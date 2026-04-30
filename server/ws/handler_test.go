package ws_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/peersh/peersh/core/auth/psk"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
	"github.com/peersh/peersh/server/ws"
)

// startTestServer wires a memory store + permissive rate-limits + an httptest
// server fronting ws.Server, returns the wsURL plus a test PSK for "alice".
func startTestServer(t *testing.T) (wsURL string, secret []byte, st store.Store, cleanup func()) {
	t.Helper()

	st = memory.New()
	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := st.PutPSKRecord(context.Background(), store.PSKRecord{
		UserID: "alice", Secret: secret, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}

	provider := psk.New(st)
	registry := room.New()
	srv := ws.New(&ws.Server{
		Store:       st,
		Auth:        provider,
		Registry:    registry,
		IPLimit:     ratelimit.New(1000, 1000),
		UserLimit:   ratelimit.New(1000, 1000),
		DeviceLimit: ratelimit.New(1000, 1000),
		ServerID:    "test/0.1",
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	hs := httptest.NewServer(mux)
	wsURL = "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	cleanup = func() {
		hs.Close()
	}
	return
}

func dialClient(t *testing.T, wsURL string, secret []byte, deviceID string, kind signalv1.DeviceKind) *signaling.Client {
	t.Helper()
	c, err := signaling.Dial(context.Background(), signaling.DialOptions{
		URL:         wsURL,
		UserID:      "alice",
		Secret:      secret,
		DeviceID:    deviceID,
		PublicKey:   []byte(deviceID + "-pub"),
		Kind:        kind,
		DisplayName: deviceID,
		ClientID:    "test-client",
	})
	if err != nil {
		t.Fatalf("signaling.Dial(%s): %v", deviceID, err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestRegisterAndForwardConnect(t *testing.T) {
	wsURL, secret, _, cleanup := startTestServer(t)
	defer cleanup()

	host := dialClient(t, wsURL, secret, "HOST00000000000A", signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST)
	cli := dialClient(t, wsURL, secret, "CLI000000000000A", signalv1.DeviceKind_DEVICE_KIND_CLI)

	// Initiator → host.
	cands := []*signalv1.EndpointCandidate{{Address: "192.168.1.5", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := cli.SendConnect(ctx, "HOST00000000000A", cands); err != nil {
		t.Fatalf("cli.SendConnect: %v", err)
	}
	got, err := host.Recv(ctx)
	if err != nil {
		t.Fatalf("host.Recv: %v", err)
	}
	if got.GetFromDeviceId() != "CLI000000000000A" {
		t.Fatalf("expected from=CLI, got %q", got.GetFromDeviceId())
	}
	if len(got.GetCandidates()) != 1 || got.GetCandidates()[0].GetAddress() != "192.168.1.5" {
		t.Fatalf("candidates not forwarded: %+v", got.GetCandidates())
	}

	// Host → initiator.
	hostCands := []*signalv1.EndpointCandidate{{Address: "10.0.0.7", Port: 7777, Type: signalv1.CandidateType_CANDIDATE_TYPE_HOST}}
	if err := host.SendConnect(ctx, "CLI000000000000A", hostCands); err != nil {
		t.Fatalf("host.SendConnect: %v", err)
	}
	back, err := cli.Recv(ctx)
	if err != nil {
		t.Fatalf("cli.Recv: %v", err)
	}
	if back.GetFromDeviceId() != "HOST00000000000A" {
		t.Fatalf("expected from=HOST, got %q", back.GetFromDeviceId())
	}
	if len(back.GetCandidates()) != 1 || back.GetCandidates()[0].GetAddress() != "10.0.0.7" {
		t.Fatalf("host candidates not forwarded: %+v", back.GetCandidates())
	}
}

func TestForwardCrossUserBlocked(t *testing.T) {
	// One server, two PSK records (different users), each with a registered
	// device. A Connect from device-A targeting device-B (different user)
	// should fail with target_unknown (the lookup is scoped to the sender's
	// user id, so the other user's device is invisible).
	st := memory.New()
	secretA := mustRandom(t, 32)
	secretB := mustRandom(t, 32)
	ctx := context.Background()
	if err := st.PutPSKRecord(ctx, store.PSKRecord{UserID: "alice", Secret: secretA, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutPSKRecord(ctx, store.PSKRecord{UserID: "bob", Secret: secretB, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	provider := psk.New(st)
	srv := ws.New(&ws.Server{
		Store:       st,
		Auth:        provider,
		Registry:    room.New(),
		IPLimit:     ratelimit.New(1000, 1000),
		UserLimit:   ratelimit.New(1000, 1000),
		DeviceLimit: ratelimit.New(1000, 1000),
	})
	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	hs := httptest.NewServer(mux)
	defer hs.Close()
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"

	alice, err := signaling.Dial(ctx, signaling.DialOptions{
		URL: wsURL, UserID: "alice", Secret: secretA, DeviceID: "ALICE0000000000A",
		Kind: signalv1.DeviceKind_DEVICE_KIND_CLI, ClientID: "alice",
	})
	if err != nil {
		t.Fatalf("alice dial: %v", err)
	}
	defer alice.Close()

	bobClient, err := signaling.Dial(ctx, signaling.DialOptions{
		URL: wsURL, UserID: "bob", Secret: secretB, DeviceID: "BOB000000000000A",
		Kind: signalv1.DeviceKind_DEVICE_KIND_WINDOWS_HOST, ClientID: "bob",
	})
	if err != nil {
		t.Fatalf("bob dial: %v", err)
	}
	defer bobClient.Close()

	// Alice tries to reach Bob's device id — server's lookup is scoped to
	// alice's user, so Bob is invisible: expect a ServerError → client
	// closes with that as the close error.
	if err := alice.SendConnect(ctx, "BOB000000000000A", nil); err != nil {
		t.Fatalf("alice send: %v", err)
	}
	rcvCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, recvErr := alice.Recv(rcvCtx)
	if recvErr == nil {
		t.Fatal("expected error, got none")
	}
}

func mustRandom(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return b
}
