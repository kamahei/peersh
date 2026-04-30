// Command peershd is the peersh Windows host.
//
// Phase 1: console app, no Windows Service registration. Listens for QUIC
// connections from peersh clients on the same LAN, completes the
// ClientHello/ServerHello handshake on stream 0, then for each subsequent
// stream reads one ExecRequest and streams ExecResponse messages back from a
// long-lived PowerShell session.
package main

import (
	"context"
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
	"syscall"

	"github.com/peersh/peersh/core/auth/none"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/windows/pwsh"
)

const protocolVersion = 1

func main() {
	listen := flag.String("listen", ":7777", "UDP address to listen on")
	certDir := flag.String("cert-dir", "", "directory for self-signed dev cert (default: platform-specific app data dir)")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if *certDir == "" {
		*certDir = defaultCertDir()
	}

	if err := run(*listen, *certDir); err != nil {
		slog.Error("peershd exiting on error", "err", err)
		os.Exit(1)
	}
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

func run(listen, certDir string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cert, err := devtls.LoadOrGenerate(certDir)
	if err != nil {
		return fmt.Errorf("load cert: %w", err)
	}
	slog.Info("dev cert ready", "dir", certDir, "self_signed_only", devtls.DevSelfSignedOnly)

	udpAddr, err := net.ResolveUDPAddr("udp", listen)
	if err != nil {
		return fmt.Errorf("resolve listen %q: %w", listen, err)
	}
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("listen udp %q: %w", listen, err)
	}
	defer pc.Close()
	slog.Info("listening", "addr", pc.LocalAddr())

	tr := transport.New(pc, devtls.ServerTLSConfig(cert))
	defer tr.Close()

	listener, err := tr.Listen(ctx)
	if err != nil {
		return fmt.Errorf("transport.Listen: %w", err)
	}
	defer listener.Close()

	// auth/none and the in-memory store are wired in for shape, even though
	// Phase 1 does not exercise them. They establish the interfaces that
	// later phases plug into without rewiring this main package.
	authProvider := none.New()
	_ = authProvider // referenced to enforce interface presence; Phase 2 will wire it.

	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
				slog.Info("listener stopped", "reason", err)
				return nil
			}
			return fmt.Errorf("Accept: %w", err)
		}
		go serveConn(ctx, conn)
	}
}

// serveConn handles one QUIC connection: Hello on the control stream, then a
// fresh per-command stream for each ExecRequest.
func serveConn(ctx context.Context, conn *transport.Conn) {
	remote := conn.RemoteAddr().String()
	log := slog.With("peer", remote)
	log.Info("connection accepted")
	defer func() {
		_ = conn.CloseWithError(0, "")
		log.Info("connection closed")
	}()

	// Control stream — must be the first stream the client opens.
	ctrl, err := conn.AcceptStream(ctx)
	if err != nil {
		log.Warn("AcceptStream(control): connection ending", "err", err)
		return
	}
	if err := doHandshake(ctrl); err != nil {
		log.Warn("handshake failed", "err", err)
		return
	}
	log.Info("handshake complete")

	host, err := pwsh.Start(ctx)
	if err != nil {
		log.Error("pwsh.Start failed", "err", err)
		return
	}
	defer func() {
		if err := host.Close(); err != nil {
			log.Warn("pwsh.Close error", "err", err)
		}
	}()
	log.Info("pwsh host started", "path", host.Path())

	// Subsequent streams are per-command.
	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			log.Info("AcceptStream: end of connection", "err", err)
			return
		}
		go serveExec(ctx, host, stream, log)
	}
}

// doHandshake runs ClientHello/ServerHello on the supplied control stream.
func doHandshake(ctrl *transport.Stream) error {
	r := wire.NewReader(ctrl)
	hello := &v1.ClientHello{}
	if err := wire.Read(r, hello); err != nil {
		return fmt.Errorf("read ClientHello: %w", err)
	}
	if hello.GetProtocolVersion() != protocolVersion {
		_ = wire.Write(ctrl, &v1.ServerHello{ProtocolVersion: protocolVersion, ServerId: "peershd/0.1"})
		return fmt.Errorf("client protocol_version=%d, server expects %d",
			hello.GetProtocolVersion(), protocolVersion)
	}
	if err := wire.Write(ctrl, &v1.ServerHello{
		ProtocolVersion: protocolVersion,
		Capabilities:    nil,
		ServerId:        "peershd/0.1",
	}); err != nil {
		return fmt.Errorf("write ServerHello: %w", err)
	}
	return nil
}

// serveExec reads one ExecRequest from stream and streams ExecResponse
// messages back until the command completes.
func serveExec(ctx context.Context, host *pwsh.Host, stream *transport.Stream, log *slog.Logger) {
	defer stream.Close()
	r := wire.NewReader(stream)
	req := &v1.ExecRequest{}
	if err := wire.Read(r, req); err != nil {
		log.Warn("read ExecRequest", "err", err)
		return
	}
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
