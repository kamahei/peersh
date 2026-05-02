package ws_test

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/peersh/peersh/core/auth/psk"
	"github.com/peersh/peersh/core/devid"
	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
	"github.com/peersh/peersh/core/signaling"
	"github.com/peersh/peersh/core/store"
	"github.com/peersh/peersh/core/store/memory"
	"github.com/peersh/peersh/server/ratelimit"
	"github.com/peersh/peersh/server/room"
	"github.com/peersh/peersh/server/ws"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// startTestServerWithIdle is like startTestServer but exposes the
// Server.IdleTimeout knob for the v2-E defense-layer tests.
func startTestServerWithIdle(t *testing.T, idle time.Duration) (string, []byte, *ws.Metrics, func()) {
	t.Helper()

	st := memory.New()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := st.PutPSKRecord(context.Background(), store.PSKRecord{
		UserID: "alice", Secret: secret, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("PutPSKRecord: %v", err)
	}

	metrics := ws.NewMetrics()
	srv := ws.New(&ws.Server{
		Store:       st,
		Auth:        psk.New(st),
		Registry:    room.New(),
		IPLimit:     ratelimit.New(1000, 1000),
		UserLimit:   ratelimit.New(1000, 1000),
		DeviceLimit: ratelimit.New(1000, 1000),
		ServerID:    "test/0.1",
		IdleTimeout: idle,
		Metrics:     metrics,
	})
	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	hs := httptest.NewServer(mux)
	wsURL := "ws" + strings.TrimPrefix(hs.URL, "http") + "/ws"
	return wsURL, secret, metrics, hs.Close
}

func dialIdleClient(t *testing.T, wsURL string, secret []byte, label string) *signaling.Client {
	t.Helper()
	pub := []byte(label + "-pub")
	deviceID := devid.Derive(pub)
	c, err := signaling.Dial(context.Background(), signaling.DialOptions{
		URL:         wsURL,
		UserID:      "alice",
		Secret:      secret,
		DeviceID:    deviceID,
		PublicKey:   pub,
		Kind:        signalv1.DeviceKind_DEVICE_KIND_MOBILE_CLIENT,
		DisplayName: label,
		ClientID:    "test/0.1",
	})
	if err != nil {
		t.Fatalf("signaling.Dial: %v", err)
	}
	return c
}

func TestServer_IdleTimeoutClosesConnection(t *testing.T) {
	idle := 200 * time.Millisecond
	wsURL, secret, metrics, cleanup := startTestServerWithIdle(t, idle)
	defer cleanup()

	c := dialIdleClient(t, wsURL, secret, "alice")
	defer c.Close()

	// Wait longer than the idle window without sending anything; the
	// server should close us with an idle_timeout ServerError.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.Recv(ctx)
	if err == nil {
		t.Fatal("expected idle close error, got nil")
	}
	if !strings.Contains(err.Error(), "idle_timeout") {
		t.Errorf("expected error to mention idle_timeout; got %v", err)
	}

	if got := testutil.ToFloat64(metrics.IdleClosed); got != 1 {
		t.Errorf("IdleClosed counter = %v; want 1", got)
	}
}

func TestServer_IdleTimeoutResetByActivity(t *testing.T) {
	idle := 500 * time.Millisecond
	wsURL, secret, metrics, cleanup := startTestServerWithIdle(t, idle)
	defer cleanup()

	c := dialIdleClient(t, wsURL, secret, "bob")
	defer c.Close()

	// Three SendConnect calls, each spaced under the idle window. The
	// server should reset the read deadline on every received frame,
	// so the connection must still be alive at the end.
	ctx := context.Background()
	target := devid.Derive([]byte("nonexistent-host"))
	for i := 0; i < 3; i++ {
		if err := c.SendConnect(ctx, target, nil); err != nil {
			t.Fatalf("SendConnect %d: %v", i, err)
		}
		time.Sleep(idle / 2)
	}

	// What we MUST NOT see is an idle_timeout — activity reset the
	// deadline. (target_unknown ServerError is fine; we ignore it.)
	if got := testutil.ToFloat64(metrics.IdleClosed); got != 0 {
		t.Fatalf("IdleClosed = %v; want 0 (activity should keep us alive)", got)
	}
}

func TestServer_IdleTimeoutDisabledByNegativeValue(t *testing.T) {
	wsURL, secret, metrics, cleanup := startTestServerWithIdle(t, -1)
	defer cleanup()

	c := dialIdleClient(t, wsURL, secret, "carol")
	defer c.Close()

	// With idle disabled, a short Recv times out from our side, not
	// from the server.
	rcvCtx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	_, err := c.Recv(rcvCtx)
	if err == nil {
		t.Fatal("expected ctx-driven Recv timeout")
	}
	if strings.Contains(err.Error(), "idle_timeout") {
		t.Fatalf("server tore us down despite IdleTimeout = -1: %v", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected client-side deadline; got %v", err)
	}
	if got := testutil.ToFloat64(metrics.IdleClosed); got != 0 {
		t.Errorf("IdleClosed = %v; want 0 when timeout disabled", got)
	}
}
