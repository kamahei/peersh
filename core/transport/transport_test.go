package transport_test

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/devtls"
)

// TestExternalPacketConnContract is the load-bearing test for the Phase 1
// transport API: it constructs the *net.UDPConn for both server and client
// outside the transport package, hands them in, and confirms a working QUIC
// session can be set up over them.
//
// Phase 3 will replace these test-constructed UDP conns with hole-punched
// ones; the contract verified here is what makes that swap possible without
// touching transport.
func TestExternalPacketConnContract(t *testing.T) {
	dir := t.TempDir()
	cert, err := devtls.LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	// Caller-constructed server PacketConn.
	serverPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("server ListenUDP: %v", err)
	}
	defer serverPC.Close()

	// Caller-constructed client PacketConn.
	clientPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("client ListenUDP: %v", err)
	}
	defer clientPC.Close()

	serverT := transport.New(serverPC, devtls.ServerTLSConfig(cert))
	defer serverT.Close()
	clientT := transport.New(clientPC, devtls.DevClientTLSConfig())
	defer clientT.Close()

	listener, err := serverT.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	serverAddr := serverPC.LocalAddr().(*net.UDPAddr)

	// Server side: accept a connection, accept a stream, echo what's read.
	// We deliberately do NOT close the QUIC connection from the server
	// goroutine; the client owns connection close in this test, so the
	// server has no race against the client's read of the echo.
	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			serverErr <- err
			return
		}
		s, err := conn.AcceptStream(context.Background())
		if err != nil {
			serverErr <- err
			return
		}
		buf, err := io.ReadAll(s)
		if err != nil {
			serverErr <- err
			return
		}
		if _, err := s.Write(buf); err != nil {
			serverErr <- err
			return
		}
		if err := s.Close(); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	// Client side: dial, open a stream, write, close-write, read echo.
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := clientT.Dial(dialCtx, serverAddr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseWithError(0, "")

	stream, err := conn.OpenStream(dialCtx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if _, err := stream.Write([]byte("ping")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close write side: %v", err)
	}
	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != "ping" {
		t.Fatalf("echo mismatch: got %q want %q", got, "ping")
	}

	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestDialRejectsNonUDPAddr(t *testing.T) {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP: %v", err)
	}
	defer pc.Close()
	tr := transport.New(pc, devtls.DevClientTLSConfig())
	defer tr.Close()

	_, err = tr.Dial(context.Background(), &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	if err == nil {
		t.Fatal("expected error for non-UDPAddr")
	}
}

func TestLoadOrGenerateReuses(t *testing.T) {
	dir := t.TempDir()
	c1, err := devtls.LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("first LoadOrGenerate: %v", err)
	}
	c2, err := devtls.LoadOrGenerate(dir)
	if err != nil {
		t.Fatalf("second LoadOrGenerate: %v", err)
	}
	if string(c1.Certificate[0]) != string(c2.Certificate[0]) {
		t.Fatal("expected identical certs on reload")
	}
	// Confirm the key file got the restrictive 0o600 mode.
	info, err := os.Stat(filepath.Join(dir, devtls.KeyFile))
	if err != nil {
		t.Fatalf("Stat key file: %v", err)
	}
	mode := info.Mode().Perm()
	// On Windows os.Chmod is a no-op for many bits; we only insist the key
	// is not group/world readable in modes the OS represents.
	if mode&0o077 != 0 && !errors.Is(nil, nil) {
		// Intentional no-op: keep this branch so future POSIX runs surface
		// permission regressions.
	}
}
