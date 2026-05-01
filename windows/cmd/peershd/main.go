// Command peershd is the peersh Windows host.
//
// Two operating modes coexist:
//
//   - Direct (Phase 1). The host accepts QUIC dials on -listen. The peer
//     must already know the address. No auth.
//
//   - Signaling-mediated (Phase 2). The host registers with a signaling
//     server, waits for incoming Connect messages from peers under the
//     same PSK user_id, replies with its own local candidates, and the
//     peer dials the existing QUIC listener.
//
// Both modes share the same QUIC listener; the signaling integration is
// purely a discovery / address-exchange overlay.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/peersh/peersh/core/devid"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/punching"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
	"sync"

	"github.com/peersh/peersh/windows/ptyhost"
	"github.com/peersh/peersh/windows/pwsh"
)

const protocolVersion = 2

// supportedCapabilities is the capability list peershd advertises in
// ServerHello. "pty.v1" tells the client it may open StreamRequest{pty: ...}
// streams; "files.v1" enables the Tier 2 session-scoped file API.
var supportedCapabilities = []string{"pty.v1", "files.v1"}

func main() {
	// Phase 7: detect Windows-Service install / uninstall / SCM-dispatch
	// modes first. runService returns handled=true when the binary
	// performed (or is performing) a service action, in which case main
	// should exit here.
	handled, err := runService(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if handled {
		return
	}

	taskHandled, err := runLogonTask(os.Args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if taskHandled {
		return
	}

	if err := runWithCtx(nil, os.Args[1:]); err != nil {
		slog.Error("peershd exiting on error", "err", err)
		os.Exit(1)
	}
}

// runWithCtx is the body of the original main(). It is called both by
// the interactive entry point (with ctx == nil; signal.NotifyContext
// produces its own) and by the Windows Service program.Start handler
// (with a context that the SCM cancels at Stop time).
func runWithCtx(serviceCtx context.Context, args []string) error {
	fs := flag.NewFlagSet("peershd", flag.ExitOnError)
	listen := fs.String("listen", ":7777", "UDP address to listen on for QUIC")
	certDir := fs.String("cert-dir", "", "directory for self-signed dev cert (default: platform-specific app data dir)")
	debug := fs.Bool("debug", false, "enable debug logging")
	signalingURL := fs.String("signaling", "", "signaling server URL (ws:// or wss://); empty disables signaling")
	userID := fs.String("user", "", "user_id under which to register (signaling mode)")
	pskFile := fs.String("psk-file", "", "path to a file containing a hex-encoded PSK (signaling mode)")
	displayName := fs.String("display-name", "", "display name to register (defaults to hostname)")
	stunServer := fs.String("stun", punching.DefaultSTUNServer, "STUN server for srflx discovery; empty disables STUN")
	if err := fs.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if *certDir == "" {
		*certDir = defaultCertDir()
	}

	return run(serviceCtx, *listen, *certDir, *signalingURL, *userID, *pskFile, *displayName, *stunServer)
}

func defaultCertDir() string {
	if runtime.GOOS == "windows" {
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return filepath.Join(v, "peersh", "dev")
		}
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return filepath.Join(v, "peersh", "dev")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "share", "peersh", "dev")
	}
	return filepath.Join(".", "peersh-dev")
}

func run(serviceCtx context.Context, listen, certDir, signalingURL, userID, pskFile, displayName, stunServer string) error {
	var ctx context.Context
	var stop func()
	if serviceCtx != nil {
		// Running under SCM: the parent owns the cancellation. Wrap it
		// so we can still observe the OS signals interactively, but
		// SCM Stop is the primary trigger.
		ctx = serviceCtx
		stop = func() {}
	} else {
		ctx, stop = signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	}
	defer stop()

	cert, err := devtls.LoadOrGenerate(certDir)
	if err != nil {
		return fmt.Errorf("load cert: %w", err)
	}
	pub, err := publicKeyFromCert(cert)
	if err != nil {
		return fmt.Errorf("extract public key: %w", err)
	}
	deviceID := devid.Derive(pub)
	slog.Info("dev cert ready", "dir", certDir, "device_id", deviceID, "self_signed_only", devtls.DevSelfSignedOnly)

	udpAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return fmt.Errorf("resolve listen %q: %w", listen, err)
	}
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp %q: %w", listen, err)
	}
	defer pc.Close()
	listenAddr := pc.LocalAddr().(*net.UDPAddr)
	slog.Info("listening for QUIC", "addr", listenAddr)

	// STUN runs BEFORE Transport.New takes over reads on pc. The discovered
	// srflx is cached and emitted as a SERVER_REFLEXIVE candidate on every
	// Connect reply. For cone NATs (the common case) one srflx port works
	// for any peer destination; symmetric NATs are the documented fail case.
	var srflx *net.UDPAddr
	if signalingURL != "" && stunServer != "" {
		stunCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		srflx, err = punching.Discover(stunCtx, pc, punching.Options{STUNServer: stunServer})
		cancel()
		if err != nil {
			slog.Warn("stun discover failed; continuing without srflx candidate", "err", err)
		}
	}

	tr := transport.New(pc, devtls.ServerTLSConfig(cert))
	defer tr.Close()
	listener, err := tr.Listen(ctx)
	if err != nil {
		return fmt.Errorf("transport.Listen: %w", err)
	}
	defer listener.Close()

	// Phase 6: SessionManager keeps pwsh.Host instances alive across
	// QUIC reconnects so a client presenting a known session_id resumes
	// where it left off (cwd, variables intact). Idle sessions are
	// reaped after pwsh.DefaultIdleTimeout.
	mgr := pwsh.NewSessionManager()
	defer mgr.Close()
	go mgr.Run(ctx)

	// Optional signaling-mode goroutine.
	if signalingURL != "" {
		if userID == "" || pskFile == "" {
			return errors.New("-signaling requires -user and -psk-file")
		}
		secret, err := readPSKFile(pskFile)
		if err != nil {
			return fmt.Errorf("read psk: %w", err)
		}
		if displayName == "" {
			displayName, _ = os.Hostname()
		}
		go runSignaling(ctx, signalingURL, userID, secret, deviceID, pub, displayName, listenAddr, srflx, pc)
	}

	// Phase 1 QUIC accept loop.
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				slog.Info("listener stopped", "reason", err)
				return nil
			}
			return fmt.Errorf("Accept: %w", err)
		}
		go serveConn(ctx, conn, mgr)
	}
}

// runSignaling dials the signaling server, registers, and replies to
// incoming Connect messages with our local candidates. The actual data
// connection arrives separately at the QUIC listener.
//
// srflx, if non-nil, is included as a SERVER_REFLEXIVE candidate. pc is the
// shared QUIC UDP socket; punching writes to it concurrently with QUIC reads.
func runSignaling(ctx context.Context, url, userID string, secret []byte, deviceID string, pub ed25519.PublicKey, displayName string, listenAddr *net.UDPAddr, srflx *net.UDPAddr, pc net.PacketConn) {
	log := slog.With("signaling", url, "user", userID, "device", deviceID)
	if srflx != nil {
		log.Info("srflx ready for advertisement", "srflx", srflx)
	}
	sc, err := signaling.Dial(ctx, signaling.DialOptions{
		URL:         url,
		UserID:      userID,
		Secret:      secret,
		DeviceID:    deviceID,
		PublicKey:   pub,
		Kind:        signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST,
		DisplayName: displayName,
		ClientID:    "peershd/0.1",
	})
	if err != nil {
		log.Error("signaling dial failed", "err", err)
		return
	}
	defer sc.Close()
	log.Info("registered with signaling server", "server_id", sc.ServerID())

	for {
		conn, err := sc.Recv(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Info("signaling closed", "err", err)
			return
		}
		from := conn.GetFromDeviceId()
		log.Info("connect request received", "from", from, "candidates", len(conn.GetCandidates()))

		// Punch the peer's candidates first so our NAT installs the mapping
		// for their address before they QUIC-dial us. Sorted by preferred
		// order; we punch all of them cheaply (5 packets each).
		peerCands := punching.SortCandidates(conn.GetCandidates())
		if err := punching.Punch(ctx, pc, punching.CandidatesToUDPAddrs(peerCands), punching.Options{}); err != nil {
			log.Warn("punch failed", "err", err)
		}

		// Reply with our local candidates so the peer can dial us.
		cands := enumerateCandidates(listenAddr, srflx)
		if err := sc.SendConnect(ctx, from, cands); err != nil {
			log.Warn("send Connect reply", "err", err)
			continue
		}
		log.Info("sent local candidates", "to", from, "count", len(cands))
	}
}

// publicKeyFromCert pulls the ed25519 public key out of a self-signed dev cert.
func publicKeyFromCert(cert tls.Certificate) (ed25519.PublicKey, error) {
	if len(cert.Certificate) == 0 {
		return nil, errors.New("empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, err
	}
	pub, ok := leaf.PublicKey.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("expected ed25519 public key, got %T", leaf.PublicKey)
	}
	return pub, nil
}

// readPSKFile reads a hex-encoded PSK from disk. Whitespace is trimmed.
func readPSKFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	s := strings.TrimSpace(string(raw))
	out, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("psk file %q: %w", path, err)
	}
	return out, nil
}

// enumerateCandidates returns the candidate list peershd advertises in its
// Connect reply. Order:
//   - SRFLX (if STUN succeeded), one entry.
//   - HOST candidates: either the bound IP, or every non-loopback /
//     non-link-local interface IP.
//
// SortCandidates on the receiver side reshuffles by IPv6/IPv4 within
// SRFLX/HOST; ordering here is purely for log readability.
func enumerateCandidates(listen *net.UDPAddr, srflx *net.UDPAddr) []*signalv1.EndpointCandidate {
	port := uint32(listen.Port)
	var out []*signalv1.EndpointCandidate
	if srflx != nil {
		out = append(out, &signalv1.EndpointCandidate{
			Address: srflx.IP.String(), Port: uint32(srflx.Port),
			Type: signalv1.CandidateType_CANDIDATE_TYPE_SERVER_REFLEXIVE,
		})
	}
	if !listen.IP.IsUnspecified() {
		out = append(out, &signalv1.EndpointCandidate{
			Address: listen.IP.String(), Port: port, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST,
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

// serveConn handles one QUIC connection: Hello (with optional reattach)
// on the control stream, then a fresh per-command stream for each
// ExecRequest. The host's pwsh process is owned by mgr, not by this
// function — when the QUIC connection closes, the session is detached
// (mgr's idle timer takes over) instead of killed.
func serveConn(ctx context.Context, conn *transport.Conn, mgr *pwsh.SessionManager) {
	remote := conn.RemoteAddr().String()
	log := slog.With("peer", remote)
	log.Info("connection accepted")
	defer func() {
		_ = conn.CloseWithError(0, "")
		log.Info("connection closed")
	}()

	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Warn("AcceptStream(control): connection ending", "err", err)
		return
	}

	sessionID, host, err := doHandshake(ctx, ctrl, mgr)
	if err != nil {
		log.Warn("handshake failed", "err", err)
		return
	}
	defer mgr.Detach(sessionID)
	log.Info("handshake complete", "session", sessionID, "pwsh", host.Path())

	registry := newPTYRegistry()
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Info("AcceptStream: end of connection", "err", err)
			return
		}
		go serveStream(ctx, host, stream, registry, log)
	}
}

// serveStream dispatches a per-stream first frame (StreamRequest) to either
// the one-shot Exec path or the interactive PTY path. Each stream owns its
// own protocol; this function returns when the stream closes.
func serveStream(ctx context.Context, host *pwsh.Host, stream *transport.Stream, reg *ptyRegistry, log *slog.Logger) {
	defer stream.Close()
	r := wire.NewReader(stream)
	req := &v1.StreamRequest{}
	if err := wire.Read(r, req); err != nil {
		log.Warn("read StreamRequest", "err", err)
		return
	}
	switch kind := req.GetKind().(type) {
	case *v1.StreamRequest_Exec:
		serveExecStream(ctx, host, stream, kind.Exec, log)
	case *v1.StreamRequest_Pty:
		servePTYStream(ctx, stream, r, kind.Pty, reg, log)
	case *v1.StreamRequest_Files:
		serveFilesStream(stream, kind.Files, reg)
	default:
		log.Warn("StreamRequest with no kind set")
	}
}

func servePTYStream(ctx context.Context, stream *transport.Stream, r *bufio.Reader, req *v1.PTYRequest, reg *ptyRegistry, log *slog.Logger) {
	clog := log.With("stream", stream.StreamID(), "pty_cmd", req.GetCommand(), "pty_id", req.GetPtyId())
	cols := uint16(req.GetCols())
	rows := uint16(req.GetRows())
	clog.Info("pty open", "cols", cols, "rows", rows)

	sess, err := ptyhost.Open(req.GetCommand(), req.GetArgs(), cols, rows)
	if err != nil {
		clog.Warn("ptyhost.Open failed", "err", err)
		_ = wire.Write(stream, &v1.PTYFrame{Kind: &v1.PTYFrame_Exit{Exit: &v1.PTYExit{ExitCode: -1, Error: err.Error()}}})
		return
	}
	defer sess.Close()

	reg.Register(req.GetPtyId(), sess)
	defer reg.Unregister(req.GetPtyId())

	pumpCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Output direction: pump child bytes -> wire frames.
	var writeMu sync.Mutex
	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		sess.Pump(pumpCtx, func(f *v1.PTYFrame) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return wire.Write(stream, f)
		})
	}()

	// Input direction: read PTYFrames from the wire, route Input/Resize.
	for {
		frame := &v1.PTYFrame{}
		if err := wire.Read(r, frame); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				clog.Info("pty read frame", "err", err)
			}
			break
		}
		switch k := frame.GetKind().(type) {
		case *v1.PTYFrame_Input:
			if _, err := sess.Write(k.Input.GetData()); err != nil {
				clog.Info("pty write to child", "err", err)
				break
			}
		case *v1.PTYFrame_Resize:
			if err := sess.Resize(uint16(k.Resize.GetCols()), uint16(k.Resize.GetRows())); err != nil {
				clog.Info("pty resize", "err", err)
			}
		default:
			// Server-bound frames (Data / Exit) on the input direction
			// are protocol violations; ignore.
		}
	}

	cancel()
	<-pumpDone
	clog.Info("pty closed")
}

func doHandshake(ctx context.Context, ctrl *transport.Stream, mgr *pwsh.SessionManager) (string, *pwsh.Host, error) {
	r := wire.NewReader(ctrl)
	hello := &v1.ClientHello{}
	if err := wire.Read(r, hello); err != nil {
		return "", nil, fmt.Errorf("read ClientHello: %w", err)
	}
	if hello.GetProtocolVersion() != protocolVersion {
		_ = wire.Write(ctrl, &v1.ServerHello{ProtocolVersion: protocolVersion, ServerId: "peershd/0.1"})
		return "", nil, fmt.Errorf("client protocol_version=%d, server expects %d",
			hello.GetProtocolVersion(), protocolVersion)
	}
	id, host, reattached, err := mgr.AttachOrCreate(ctx, hello.GetSessionId())
	if err != nil {
		return "", nil, fmt.Errorf("AttachOrCreate: %w", err)
	}
	if err := wire.Write(ctrl, &v1.ServerHello{
		ProtocolVersion: protocolVersion,
		Capabilities:    supportedCapabilities,
		ServerId:        "peershd/0.1",
		SessionId:       id,
		Reattached:      reattached,
	}); err != nil {
		return "", nil, fmt.Errorf("write ServerHello: %w", err)
	}
	return id, host, nil
}

func serveExecStream(ctx context.Context, host *pwsh.Host, stream *transport.Stream, req *v1.ExecRequest, log *slog.Logger) {
	cmd := req.GetCommand()
	clog := log.With("stream", stream.StreamID(), "cmd_len", len(cmd))
	clog.Info("exec received")

	out, err := host.Exec(ctx, cmd)
	if err != nil {
		clog.Warn("pwsh Exec error", "err", err)
		return
	}

	for {
		c, err := out.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			clog.Warn("Output.Recv error", "err", err)
			return
		}
		resp := &v1.ExecResponse{}
		if c.Stream == pwsh.Stderr {
			resp.Chunk = &v1.ExecResponse_Stderr{Stderr: c.Data}
		} else {
			resp.Chunk = &v1.ExecResponse_Stdout{Stdout: c.Data}
		}
		if err := wire.Write(stream, resp); err != nil {
			clog.Warn("write ExecResponse chunk", "err", err)
			return
		}
	}
	if err := wire.Write(stream, &v1.ExecResponse{Done: true}); err != nil {
		clog.Warn("write ExecResponse done", "err", err)
		return
	}
	clog.Info("exec done")
}
