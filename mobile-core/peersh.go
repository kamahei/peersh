package peersh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
	"github.com/peersh/peersh/core/wire"
)

// Build is updated when a new mobile-core release is cut. The Flutter app
// reads this via Version() to surface in About / debug screens.
const Build = "mobile-core/0.1+phase4a"

// Version returns the mobile-core build identifier. Smoke test for "is the
// gomobile bind alive at all".
func Version() string { return Build }

// Echo dials a peersh host directly at addr (host:port), runs the
// QUIC ClientHello/ServerHello on stream 0, sends one ExecRequest with
// the given command on a fresh stream, drains stdout/stderr, and returns
// the concatenated stdout text.
//
// Failures are returned as the string "ERROR: " + reason. This sacrifices
// type safety for gomobile-friendliness (no errors crossing the bind
// boundary).
func Echo(addr string, command string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	out, err := echo(ctx, addr, command)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return out
}

func echo(ctx context.Context, addr, command string) (string, error) {
	uaddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", addr, err)
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return "", fmt.Errorf("ListenUDP: %w", err)
	}
	defer pc.Close()

	tr := transport.New(pc, devtls.DevClientTLSConfig())
	defer tr.Close()

	conn, err := tr.Dial(ctx, uaddr)
	if err != nil {
		return "", fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseWithError(0, "")

	// Hello on the control stream.
	ctrl, err := conn.OpenStream(ctx)
	if err != nil {
		return "", fmt.Errorf("control stream: %w", err)
	}
	if err := wire.Write(ctrl, &v1.ClientHello{ProtocolVersion: 1, ClientId: "mobile-core"}); err != nil {
		return "", fmt.Errorf("write ClientHello: %w", err)
	}
	_ = ctrl.Close()
	srv := &v1.ServerHello{}
	if err := wire.Read(wire.NewReader(ctrl), srv); err != nil {
		return "", fmt.Errorf("read ServerHello: %w", err)
	}

	// Per-command stream.
	exec, err := conn.OpenStream(ctx)
	if err != nil {
		return "", fmt.Errorf("exec stream: %w", err)
	}
	defer exec.Close()
	if err := wire.Write(exec, &v1.ExecRequest{Command: command}); err != nil {
		return "", fmt.Errorf("write ExecRequest: %w", err)
	}
	r := wire.NewReader(exec)
	var stdout []byte
	for {
		resp := &v1.ExecResponse{}
		if err := wire.Read(r, resp); err != nil {
			if errors.Is(err, io.EOF) {
				return string(stdout), nil
			}
			return "", fmt.Errorf("read ExecResponse: %w", err)
		}
		if d := resp.GetStdout(); len(d) > 0 {
			stdout = append(stdout, d...)
		}
		if resp.GetDone() {
			return string(stdout), nil
		}
	}
}
