// Command peersh-cli is a developer REPL client for peersh.
//
// Two operating modes coexist:
//
//   - Direct (Phase 1). Pass -addr <host:port> and the CLI dials QUIC
//     straight at that endpoint. No auth, no signaling.
//
//   - Signaling-mediated (Phase 2). Pass -signaling, -user, -psk-file,
//     and -target. The CLI registers with the signaling server, requests
//     a Connect to the target device, learns the host's candidates, and
//     dials QUIC at the first reachable one.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/peertls"
	"github.com/peersh/peersh/core/wire"
)

const (
	protocolVersion = 2
	clientID        = "peersh-cli/0.2"
)

func main() {
	addr := flag.String("addr", "", "(direct mode) peershd host (host:port)")
	hostDevice := flag.String("host-device", "", "(direct mode, optional) expected peershd device_id to pin at the TLS layer")

	signalingURL := flag.String("signaling", "", "(signaling mode) signaling server URL (ws:// or wss://)")
	userID := flag.String("user", "", "(signaling mode) user_id under which to register")
	pskFile := flag.String("psk-file", "", "(signaling mode) path to a file containing a hex-encoded PSK")
	target := flag.String("target", "", "(signaling mode) target peershd device_id to connect to")
	stunServer := flag.String("stun", punching.DefaultSTUNServer, "STUN server for srflx discovery; empty disables STUN")

	ptyMode := flag.Bool("pty", false, "open an interactive PTY instead of the one-shot REPL (Phase 8 Tier 1)")
	ptyCmd := flag.String("pty-cmd", "", "executable to spawn under the PTY; empty = operator-default shell")

	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if (*addr == "") == (*signalingURL == "") {
		fmt.Fprintln(os.Stderr, "exactly one of -addr or -signaling must be supplied")
		os.Exit(2)
	}
	if *hostDevice != "" && *signalingURL != "" {
		fmt.Fprintln(os.Stderr, "-host-device only applies to direct mode (-addr); use -target in signaling mode")
		os.Exit(2)
	}

	if err := run(*addr, *signalingURL, *userID, *pskFile, *target, *stunServer, *ptyMode, *ptyCmd, *hostDevice); err != nil {
		slog.Error("peersh-cli exiting on error", "err", err)
		os.Exit(1)
	}
}

func run(addr, signalingURL, userID, pskFile, target, stunServer string, ptyMode bool, ptyCmd, hostDevice string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("ListenUDP: %w", err)
	}
	defer pc.Close()

	// In signaling mode, run STUN before Transport.New takes over reads.
	var srflx *net.UDPAddr
	if signalingURL != "" && stunServer != "" {
		stunCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		srflx, err = punching.Discover(stunCtx, pc, punching.Options{STUNServer: stunServer})
		cancel()
		if err != nil {
			slog.Warn("stun discover failed; continuing without srflx candidate", "err", err)
		}
	}

	// Generate the CLI's ephemeral ed25519 keypair. The same key drives
	// both the signaling Register frame (via devid.Derive on the pubkey)
	// and the mTLS client cert presented to peershd, so the server sees
	// a consistent identity on the QUIC and signaling sides.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate device key: %w", err)
	}
	deviceID := devid.Derive(pub)
	clientCert, err := peertls.CertFromEd25519(priv)
	if err != nil {
		return fmt.Errorf("build client cert: %w", err)
	}

	// Signaling mode pins the target's device_id at the TLS layer.
	// Direct mode (-addr) pins only if the operator passed -host-device;
	// otherwise we accept any pubkey-bound server cert, which is the
	// dev workflow where the host's device_id is not known ahead of
	// time. Both modes still present a client cert because peershd
	// requires one regardless.
	pin := target
	if signalingURL == "" {
		pin = hostDevice
	}
	tr := transport.New(pc, peertls.ClientTLSConfig(clientCert, pin))
	defer tr.Close()

	if signalingURL != "" {
		if userID == "" || pskFile == "" || target == "" {
			return errors.New("-signaling requires -user, -psk-file, and -target")
		}
		secret, err := readPSKFile(pskFile)
		if err != nil {
			return fmt.Errorf("read psk: %w", err)
		}
		conn, err := rendezvousAndDial(ctx, tr, pc, signalingURL, userID, secret, target, pc.LocalAddr().(*net.UDPAddr), srflx, pub, deviceID)
		if err != nil {
			return err
		}
		defer conn.CloseWithError(0, "")
		slog.Info("connected", "remote", conn.RemoteAddr())
		if err := doHandshake(ctx, conn); err != nil {
			return fmt.Errorf("handshake: %w", err)
		}
		if ptyMode {
			return runPTY(ctx, conn, ptyCmd)
		}
		return repl(ctx, conn)
	}

	dialAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", addr, err)
	}
	conn, err := tr.Dial(ctx, dialAddr)
	if err != nil {
		return fmt.Errorf("Dial %s: %w", dialAddr, err)
	}
	defer conn.CloseWithError(0, "")
	slog.Info("connected", "remote", dialAddr)
	if err := doHandshake(ctx, conn); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	if ptyMode {
		return runPTY(ctx, conn, ptyCmd)
	}
	return repl(ctx, conn)
}

// rendezvousAndDial handles the full Phase 2 + Phase 3 signaling-mediated
// connection setup: register, send Connect, receive the host's reply,
// punch the peer's candidates to install local NAT mappings, and dial each
// candidate in preferred order until one succeeds.
//
// pub / deviceID come from the same ed25519 keypair the caller already
// installed in the QUIC mTLS client cert, so signaling Register and the
// TLS handshake advertise a consistent identity.
//
// Returns punching.ErrTraversalFailed if every candidate dial attempt
// fails.
func rendezvousAndDial(ctx context.Context, tr *transport.Transport, pc net.PacketConn, url, userID string, secret []byte, targetDeviceID string, localAddr, srflx *net.UDPAddr, pub ed25519.PublicKey, deviceID string) (*transport.Conn, error) {
	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:         url,
		UserID:      userID,
		Secret:      secret,
		DeviceID:    deviceID,
		PublicKey:   pub,
		Kind:        signalv1.DeviceKind_DEVICE_KIND_CLI,
		DisplayName: "peersh-cli",
		ClientID:    clientID,
	})
	if err != nil {
		return nil, fmt.Errorf("signaling.Dial: %w", err)
	}
	defer sc.Close()
	slog.Info("registered with signaling", "device", deviceID, "server_id", sc.ServerID())

	cands := localCandidates(localAddr, srflx)
	slog.Info("requesting connect", "target", targetDeviceID, "candidates", len(cands))
	if err := sc.SendConnect(ctx, targetDeviceID, cands); err != nil {
		return nil, fmt.Errorf("SendConnect: %w", err)
	}

	reply, err := sc.Recv(ctx)
	if err != nil {
		return nil, fmt.Errorf("waiting for target reply: %w", err)
	}
	if reply.GetFromDeviceId() != targetDeviceID {
		return nil, fmt.Errorf("got Connect from %q, expected from %q",
			reply.GetFromDeviceId(), targetDeviceID)
	}
	if len(reply.GetCandidates()) == 0 {
		return nil, errors.New("target returned no candidates")
	}

	sorted := punching.SortCandidates(reply.GetCandidates())
	peerAddrs := punching.CandidatesToUDPAddrs(sorted)
	slog.Info("rendezvous complete", "candidates", len(peerAddrs))

	// Punch first so our NAT installs mappings for the peer's addresses.
	if err := punching.Punch(ctx, pc, peerAddrs, punching.Options{}); err != nil {
		slog.Warn("punch failed", "err", err)
	}

	// Sequential dial in preferred order; first success wins.
	for _, p := range peerAddrs {
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := tr.Dial(dialCtx, p)
		cancel()
		if err == nil {
			slog.Info("dialed candidate", "addr", p)
			return conn, nil
		}
		slog.Info("candidate dial failed", "addr", p, "err", err)
	}
	return nil, punching.ErrTraversalFailed
}

func readPSKFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return nil, fmt.Errorf("psk file %q: %w", path, err)
	}
	return out, nil
}

// localCandidates enumerates this CLI's locally reachable IPs at the bound
// port plus an optional SRFLX candidate from STUN.
func localCandidates(local *net.UDPAddr, srflx *net.UDPAddr) []*signalv1.EndpointCandidate {
	port := uint32(local.Port)
	var out []*signalv1.EndpointCandidate
	if srflx != nil {
		out = append(out, &signalv1.EndpointCandidate{
			Address: srflx.IP.String(), Port: uint32(srflx.Port),
			Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE,
		})
	}
	if !local.IP.IsUnspecified() {
		out = append(out, &signalv1.EndpointCandidate{
			Address: local.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
		})
		return out
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() || ipnet.IP.IsLinkLocalMulticast() {
			continue
		}
		out = append(out, &signalv1.EndpointCandidate{
			Address: ipnet.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
		})
	}
	return out
}

func doHandshake(ctx context.Context, conn *transport.Conn) error {
	ctrl, err := conn.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("OpenStream(control): %w", err)
	}
	if err := wire.Write(ctrl, &v1.ClientHello{
		ProtocolVersion: protocolVersion,
		Capabilities:    nil,
		ClientId:        clientID,
	}); err != nil {
		return err
	}
	if err := ctrl.Close(); err != nil {
		slog.Warn("control stream half-close", "err", err)
	}
	r := wire.NewReader(ctrl)
	srv := &v1.ServerHello{}
	if err := wire.Read(r, srv); err != nil {
		return fmt.Errorf("read ServerHello: %w", err)
	}
	if srv.GetProtocolVersion() != protocolVersion {
		return fmt.Errorf("protocol_version mismatch: server=%d, client=%d",
			srv.GetProtocolVersion(), protocolVersion)
	}
	slog.Info("handshake complete", "server_id", srv.GetServerId())
	return nil
}

func repl(ctx context.Context, conn *transport.Conn) error {
	in := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("peersh> ")
		line, err := in.ReadString('\n')
		if errors.Is(err, io.EOF) {
			fmt.Println()
			return nil
		}
		if err != nil {
			return fmt.Errorf("stdin: %w", err)
		}
		cmd := strings.TrimRight(line, "\r\n")
		if cmd == "" {
			continue
		}
		if cmd == "exit" || cmd == "quit" {
			return nil
		}
		if err := runOnce(ctx, conn, cmd); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
}

func runOnce(ctx context.Context, conn *transport.Conn, cmd string) error {
	stream, err := conn.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("OpenStream: %w", err)
	}
	defer stream.Close()

	if err := wire.Write(stream, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Exec{Exec: &v1.ExecRequest{Command: cmd}},
	}); err != nil {
		return fmt.Errorf("write StreamRequest: %w", err)
	}

	r := wire.NewReader(stream)
	for {
		resp := &v1.ExecResponse{}
		if err := wire.Read(r, resp); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("read ExecResponse: %w", err)
		}
		if resp.GetDone() {
			return nil
		}
		if data := resp.GetStdout(); len(data) > 0 {
			os.Stdout.Write(data)
		}
		if data := resp.GetStderr(); len(data) > 0 {
			os.Stderr.Write(data)
		}
	}
}
