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

	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
)

const (
	protocolVersion = 1
	clientID        = "peersh-cli/0.1"
)

func main() {
	addr := flag.String("addr", "", "(direct mode) peershd host (host:port)")

	signalingURL := flag.String("signaling", "", "(signaling mode) signaling server URL (ws:// or wss://)")
	userID := flag.String("user", "", "(signaling mode) user_id under which to register")
	pskFile := flag.String("psk-file", "", "(signaling mode) path to a file containing a hex-encoded PSK")
	target := flag.String("target", "", "(signaling mode) target peershd device_id to connect to")

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

	if err := run(*addr, *signalingURL, *userID, *pskFile, *target); err != nil {
		slog.Error("peersh-cli exiting on error", "err", err)
		os.Exit(1)
	}
}

func run(addr, signalingURL, userID, pskFile, target string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("ListenUDP: %w", err)
	}
	defer pc.Close()

	tr := transport.New(pc, devtls.DevClientTLSConfig())
	defer tr.Close()

	var dialAddr *net.UDPAddr
	if signalingURL != "" {
		if userID == "" || pskFile == "" || target == "" {
			return errors.New("-signaling requires -user, -psk-file, and -target")
		}
		secret, err := readPSKFile(pskFile)
		if err != nil {
			return fmt.Errorf("read psk: %w", err)
		}
		dialAddr, err = rendezvous(ctx, signalingURL, userID, secret, target, pc.LocalAddr().(*net.UDPAddr))
		if err != nil {
			return fmt.Errorf("rendezvous: %w", err)
		}
	} else {
		dialAddr, err = net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return fmt.Errorf("resolve %q: %w", addr, err)
		}
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
	return repl(ctx, conn)
}

// rendezvous handles the Phase 2 signaling-mediated connection setup. It
// returns the UDPAddr that should be QUIC-dialed.
func rendezvous(ctx context.Context, url, userID string, secret []byte, targetDeviceID string, localAddr *net.UDPAddr) (*net.UDPAddr, error) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate device key: %w", err)
	}
	deviceID := devid.Derive(pub)

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

	cands := localCandidates(localAddr)
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
	chosen := reply.GetCandidates()[0]
	addr := &net.UDPAddr{IP: net.ParseIP(chosen.GetAddress()), Port: int(chosen.GetPort())}
	if addr.IP == nil {
		return nil, fmt.Errorf("invalid target address %q", chosen.GetAddress())
	}
	slog.Info("rendezvous complete", "dial", addr)
	return addr, nil
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
// port. With a wildcard bind (the default), every non-loopback / non-link-
// local interface address is emitted.
func localCandidates(local *net.UDPAddr) []*signalv1.EndpointCandidate {
	port := uint32(local.Port)
	if !local.IP.IsUnspecified() {
		return []*signalv1.EndpointCandidate{
			{Address: local.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST},
		}
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []*signalv1.EndpointCandidate
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

	if err := wire.Write(stream, &v1.ExecRequest{Command: cmd}); err != nil {
		return fmt.Errorf("write ExecRequest: %w", err)
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
