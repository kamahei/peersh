package firebase

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func TestMetrics_NilSafeNoOps(t *testing.T) {
	var m *Metrics
	// All helpers must be safe to call on a nil receiver — call sites
	// rely on this so they don't have to wrap every observation in
	// `if m != nil`.
	m.ObserveWakeReceived()
	m.ObserveWakeLatencySeconds(0.5)
	m.ObserveWSOpenSeconds(1.0)
	m.ObserveHeartbeat(true)
	m.ObserveHeartbeat(false)
	m.ObserveRtdbReconnect()
	m.SetRtdbListenerActive(true)
}

func TestMetrics_CountersIncrement(t *testing.T) {
	m := NewMetrics()
	m.ObserveWakeReceived()
	m.ObserveWakeReceived()
	if got := testutil.ToFloat64(m.WakeEventReceived); got != 2 {
		t.Errorf("WakeEventReceived = %v; want 2", got)
	}
	m.ObserveRtdbReconnect()
	if got := testutil.ToFloat64(m.RtdbListenerReconnect); got != 1 {
		t.Errorf("RtdbListenerReconnect = %v; want 1", got)
	}
}

func TestMetrics_HeartbeatLabels(t *testing.T) {
	m := NewMetrics()
	m.ObserveHeartbeat(true)
	m.ObserveHeartbeat(true)
	m.ObserveHeartbeat(false)
	if got := testutil.ToFloat64(m.Heartbeat.WithLabelValues("success")); got != 2 {
		t.Errorf("heartbeat success = %v; want 2", got)
	}
	if got := testutil.ToFloat64(m.Heartbeat.WithLabelValues("failure")); got != 1 {
		t.Errorf("heartbeat failure = %v; want 1", got)
	}
}

func TestMetrics_GaugeFlips(t *testing.T) {
	m := NewMetrics()
	m.SetRtdbListenerActive(true)
	if got := testutil.ToFloat64(m.RtdbListenerActive); got != 1 {
		t.Errorf("active = %v; want 1", got)
	}
	m.SetRtdbListenerActive(false)
	if got := testutil.ToFloat64(m.RtdbListenerActive); got != 0 {
		t.Errorf("active = %v; want 0", got)
	}
}

func TestMetrics_HistogramRecords(t *testing.T) {
	m := NewMetrics()
	m.ObserveWakeLatencySeconds(0.3)
	m.ObserveWakeLatencySeconds(2.0)
	// Negative / zero values are dropped (mobile clock skew or
	// missing created_at) — sample_count should remain 2.
	m.ObserveWakeLatencySeconds(-1)
	m.ObserveWakeLatencySeconds(0)
	if got := histogramSampleCount(t, m.WakeEventLatency); got != 2 {
		t.Errorf("sample count = %d; want 2", got)
	}
}

func TestMetrics_RegisterAddsAll(t *testing.T) {
	m := NewMetrics()
	reg := prometheus.NewRegistry()
	if err := m.Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Touch each metric so a series exists for Gather. CounterVec
	// in particular yields nothing until at least one label set has
	// been observed.
	m.ObserveWakeReceived()
	m.ObserveWakeLatencySeconds(0.1)
	m.ObserveWSOpenSeconds(0.1)
	m.ObserveHeartbeat(true)
	m.ObserveHeartbeat(false)
	m.ObserveRtdbReconnect()
	m.SetRtdbListenerActive(true)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	want := map[string]bool{
		"peersh_wake_event_received_total":     true,
		"peersh_wake_event_latency_seconds":    true,
		"peersh_signaling_ws_open_seconds":     true,
		"peersh_heartbeat_total":               true,
		"peersh_rtdb_listener_reconnect_total": true,
		"peersh_rtdb_listener_active":          true,
	}
	for _, mf := range mfs {
		delete(want, mf.GetName())
	}
	if len(want) != 0 {
		t.Errorf("missing metrics after Register: %v", want)
	}
}

// histogramSampleCount returns the histogram's sample_count by
// reading the underlying dto.Metric directly.
func histogramSampleCount(t *testing.T, h prometheus.Histogram) int {
	t.Helper()
	var m dto.Metric
	if err := h.Write(&m); err != nil {
		t.Fatalf("histogram write: %v", err)
	}
	if m.Histogram == nil {
		t.Fatal("not a histogram")
	}
	return int(m.Histogram.GetSampleCount())
}
