// Command peersh-cli is a developer REPL client for peersh.
//
// Phase 1: same-LAN direct connection, dev-only TLS (InsecureSkipVerify), no
// auth. Connects to a peershd host, completes the Hello handshake on stream
// 0, then sends each typed line as an ExecRequest on a fresh stream and
// prints the streamed stdout/stderr until done.
package main

import (
	"bufio"
	"context"
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

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
)

const (
	protocolVersion = 1
	clientID        = "peersh-cli/0.1"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7777", "peershd host (host:port)")
	debug := flag.Bool("debug", false, "enable debug logging")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if err := run(*addr); err != nil {
		slog.Error("peersh-cli exiting on error", "err", err)
		os.Exit(1)
	}
}

func run(addr string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", addr, err)
	}

	// Caller-constructed UDP socket. Phase 3 will swap this for a punched
	// socket; the transport package never creates its own PacketConn.
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("ListenUDP: %w", err)
	}
	defer pc.Close()

	tr := transport.New(pc, devtls.DevClientTLSConfig())
	defer tr.Close()

	conn, err := tr.Dial(ctx, uaddr)
	if err != nil {
		return fmt.Errorf("Dial %s: %w", uaddr, err)
	}
	defer conn.CloseWithError(0, "")
	slog.Info("connected", "remote", uaddr)

	if err := doHandshake(ctx, conn); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}

	return repl(ctx, conn)
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
		// Closing the write side signals end-of-Hello; the read continues.
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
