package ws_test

import (
	"context"
	"testing"
	"time"

	"github.com/peersh/peersh/core/devid"
	"github.com/peersh/peersh/core/signaling"
	dto "github.com/prometheus/client_model/go"
)

// histSampleCount returns the number of observations in a histogram.
// testutil.ToFloat64 doesn't work for histograms; we read the dto
// directly.
func histSampleCount(t *testing.T, h interface {
	Write(*dto.Metric) error
}) int {
	t.Helper()
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatalf("hist write: %v", err)
	}
	if m.Histogram == nil {
		t.Fatal("not a histogram")
	}
	return int(m.Histogram.GetSampleCount())
}

// histSampleSum returns the sum of all observations in a histogram.
func histSampleSum(t *testing.T, h interface {
	Write(*dto.Metric) error
}) float64 {
	t.Helper()
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatalf("hist write: %v", err)
	}
	if m.Histogram == nil {
		t.Fatal("not a histogram")
	}
	return m.Histogram.GetSampleSum()
}

func TestServer_SessionDurationRecorded(t *testing.T) {
	wsURL, secret, metrics, cleanup := startTestServerWithIdle(t, 5*time.Second)
	defer cleanup()

	// Open + immediately close a registered connection.
	c := dialIdleClient(t, wsURL, secret, "alice")
	time.Sleep(50 * time.Millisecond)
	_ = c.Close()
	// Give the server a moment to observe the close.
	time.Sleep(150 * time.Millisecond)

	if got := histSampleCount(t, metrics.SessionDuration); got != 1 {
		t.Errorf("session duration sample count = %d; want 1", got)
	}
	if got := histSampleSum(t, metrics.SessionDuration); got <= 0 {
		t.Errorf("session duration sum = %v; want > 0", got)
	}
}

func TestServer_RegisterToFirstConnectRecorded(t *testing.T) {
	wsURL, secret, metrics, cleanup := startTestServerWithIdle(t, 5*time.Second)
	defer cleanup()

	c := dialIdleClient(t, wsURL, secret, "bob")
	defer c.Close()

	// Sleep before first SendConnect so the histogram observation
	// is non-trivial.
	time.Sleep(80 * time.Millisecond)
	target := devid.Derive([]byte("not-registered-target"))
	if err := c.SendConnect(context.Background(), target, nil); err != nil {
		t.Fatalf("SendConnect: %v", err)
	}
	// The server reports target_unknown via ServerError; we ignore
	// it here. Wait for the observation to land.
	time.Sleep(150 * time.Millisecond)

	if got := histSampleCount(t, metrics.RegisterToFirstConnect); got != 1 {
		t.Errorf("register-to-first-connect sample count = %d; want 1", got)
	}
	sum := histSampleSum(t, metrics.RegisterToFirstConnect)
	if sum < 0.05 {
		t.Errorf("register-to-first-connect sum = %v; want >= 0.05 (waited 80 ms)", sum)
	}

	// A second SendConnect should NOT bump the count — first-only.
	if err := c.SendConnect(context.Background(), target, nil); err != nil {
		// SendConnect on a closed (server-rejected) connection may
		// return an error; either way the observation should not
		// double-count.
		_ = err
	}
	time.Sleep(50 * time.Millisecond)
	if got := histSampleCount(t, metrics.RegisterToFirstConnect); got != 1 {
		t.Errorf("after 2nd SendConnect sample count = %d; want still 1", got)
	}

	// Avoid unused-import warning if signaling import only used here.
	_ = signaling.ErrClosed
}

// Ensure histogram doesn't fire when handshake fails (Register
// rejected). We don't have a clean way to force handshake failure
// at the public API; leave coverage to the existing Register
// rejection tests in handler_test.go.
