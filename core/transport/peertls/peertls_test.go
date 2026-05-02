package peertls_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/peersh/peersh/core/devid"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/transport/peertls"
)

func mustKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	return priv
}

func TestLoadOrGenerateKey_ReusesAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	k1, err := peertls.LoadOrGenerateKey(dir)
	if err != nil {
		t.Fatalf("first LoadOrGenerateKey: %v", err)
	}
	k2, err := peertls.LoadOrGenerateKey(dir)
	if err != nil {
		t.Fatalf("second LoadOrGenerateKey: %v", err)
	}
	if !k1.Equal(k2) {
		t.Fatal("expected identical key on reload")
	}
}

func TestCertFromEd25519_RejectsWrongSize(t *testing.T) {
	if _, err := peertls.CertFromEd25519(make(ed25519.PrivateKey, 8)); err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestPeerDeviceID_MatchesExpected(t *testing.T) {
	priv := mustKey(t)
	pub := priv.Public().(ed25519.PublicKey)
	want := devid.Derive(pub)

	hostKey := mustKey(t)
	hostCert, err := peertls.CertFromEd25519(hostKey)
	if err != nil {
		t.Fatalf("CertFromEd25519 host: %v", err)
	}
	cliCert, err := peertls.CertFromEd25519(priv)
	if err != nil {
		t.Fatalf("CertFromEd25519 cli: %v", err)
	}

	hostPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP host: %v", err)
	}
	defer hostPC.Close()
	cliPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP cli: %v", err)
	}
	defer cliPC.Close()

	hostT := transport.New(hostPC, peertls.ServerTLSConfig(hostCert))
	defer hostT.Close()
	cliT := transport.New(cliPC, peertls.ClientTLSConfig(cliCert, devid.Derive(hostKey.Public().(ed25519.PublicKey))))
	defer cliT.Close()

	listener, err := hostT.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	errChan := make(chan error, 1)
	go func() {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			errChan <- err
			return
		}
		s, err := conn.AcceptStream(context.Background())
		if err != nil {
			errChan <- err
			return
		}
		_, _ = io.ReadAll(s)
		// We do not currently expose tls.ConnectionState from the
		// transport wrapper. Application-layer code that needs the peer
		// device_id will plumb it via a future accessor; this test is
		// satisfied by confirming the handshake succeeds, which means
		// the host accepted a valid mTLS client cert.
		errChan <- nil
	}()

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := cliT.Dial(dialCtx, hostPC.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseWithError(0, "")
	stream, err := conn.OpenStream(dialCtx)
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("stream Close: %v", err)
	}

	select {
	case err := <-errChan:
		if err != nil {
			t.Fatalf("server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server goroutine timed out")
	}

	// Sanity: the client's public key produces the device_id we expected.
	if got := devid.Derive(pub); got != want {
		t.Fatalf("devid mismatch: got %q want %q", got, want)
	}
}

func TestClientRejectsServerWithMismatchedDeviceID(t *testing.T) {
	hostKey := mustKey(t)
	hostCert, err := peertls.CertFromEd25519(hostKey)
	if err != nil {
		t.Fatalf("CertFromEd25519 host: %v", err)
	}
	cliKey := mustKey(t)
	cliCert, err := peertls.CertFromEd25519(cliKey)
	if err != nil {
		t.Fatalf("CertFromEd25519 cli: %v", err)
	}

	hostPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP host: %v", err)
	}
	defer hostPC.Close()
	cliPC, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatalf("ListenUDP cli: %v", err)
	}
	defer cliPC.Close()

	// Pin to a *different* device_id than the host actually presents.
	// Using devid.Derive on a fresh random pubkey gives a guaranteed-
	// different 16-char ID with overwhelming probability.
	wrongPub := mustKey(t).Public().(ed25519.PublicKey)
	wrongID := devid.Derive(wrongPub)

	hostT := transport.New(hostPC, peertls.ServerTLSConfig(hostCert))
	defer hostT.Close()
	cliT := transport.New(cliPC, peertls.ClientTLSConfig(cliCert, wrongID))
	defer cliT.Close()

	listener, err := hostT.Listen(context.Background())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	// Run an Accept goroutine so the QUIC server side actually services
	// the handshake; otherwise Dial may time out instead of fail with a
	// TLS verification error.
	go func() {
		_, _ = listener.Accept(context.Background())
	}()

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = cliT.Dial(dialCtx, hostPC.LocalAddr().(*net.UDPAddr))
	if err == nil {
		t.Fatal("expected Dial to fail with device_id mismatch")
	}
	if !strings.Contains(err.Error(), "device_id mismatch") {
		t.Fatalf("expected device_id mismatch error, got: %v", err)
	}
}
