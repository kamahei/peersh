// Prometheus metrics for the wake-listener path on the host side.
//
// Mirrors the style of server/ws/metrics.go: a Metrics struct held by
// peershd's runtime, registered against a private prometheus.Registry,
// with nil-safe observe* helpers so call sites never have to check
// for the disabled-metrics case (m == nil).
//
// All metric names use the "peersh_" prefix consistent with the
// signaling server. Wake-related metrics carry "wake_" or "rtdb_"
// in the name to distinguish them from server-side WS metrics.

package firebase

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the host-side Prometheus metric set. Construct via
// NewMetrics and register with Register before exposing /metrics.
type Metrics struct {
	// WakeEventReceived counts wake notifications surfaced from the
	// RTDB SSE stream to the wake-pump.
	WakeEventReceived prometheus.Counter

	// WakeEventLatency observes the elapsed time between the
	// mobile-side ServerValue.timestamp on the wake_request and the
	// instant the host's listener received it. Skipped when the
	// wake_request lacks a created_at field (older app builds).
	WakeEventLatency prometheus.Histogram

	// SignalingWSOpen observes the per-wake signaling WebSocket
	// lifetime on the host (Dial → Close).
	SignalingWSOpen prometheus.Histogram

	// Heartbeat counts RTDB last_seen_at write outcomes by result.
	Heartbeat *prometheus.CounterVec

	// RtdbListenerReconnect counts SSE stream re-establishments,
	// including proactive reconnects for token refresh.
	RtdbListenerReconnect prometheus.Counter

	// RtdbListenerActive is 0 / 1 — is the SSE stream currently
	// connected to firebasedatabase.app.
	RtdbListenerActive prometheus.Gauge
}

// NewMetrics builds the metric collectors but does not register them.
// Call Register afterwards (or pass through prometheus.MustRegister).
func NewMetrics() *Metrics {
	// Buckets for sub-second to ~minute scale: wake-event latency
	// is normally <1 s; WS open is normally <20 s but capped by
	// wakeShortTTL + wakeDrainTTL ~= 20 s. The tail buckets catch
	// pathological cold-start cases.
	durationBuckets := []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 60}
	return &Metrics{
		WakeEventReceived: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peersh_wake_event_received_total",
			Help: "Wake events the RTDB listener surfaced to the wake-pump.",
		}),
		WakeEventLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "peersh_wake_event_latency_seconds",
			Help:    "Mobile ServerValue.timestamp → host receive elapsed.",
			Buckets: durationBuckets,
		}),
		SignalingWSOpen: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "peersh_signaling_ws_open_seconds",
			Help:    "Host-side signaling WebSocket lifetime per wake (Dial → Close).",
			Buckets: durationBuckets,
		}),
		Heartbeat: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "peersh_heartbeat_total",
			Help: "RTDB last_seen_at write outcomes.",
		}, []string{"result"}),
		RtdbListenerReconnect: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peersh_rtdb_listener_reconnect_total",
			Help: "Times the RTDB SSE stream was re-established (incl. token refresh).",
		}),
		RtdbListenerActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "peersh_rtdb_listener_active",
			Help: "0 / 1 — is the RTDB SSE stream currently connected.",
		}),
	}
}

// Register adds every collector in m to the given Prometheus registry.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		m.WakeEventReceived,
		m.WakeEventLatency,
		m.SignalingWSOpen,
		m.Heartbeat,
		m.RtdbListenerReconnect,
		m.RtdbListenerActive,
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// --- nil-safe helpers --------------------------------------------------

// ObserveWakeReceived records one delivered wake event.
func (m *Metrics) ObserveWakeReceived() {
	if m == nil {
		return
	}
	m.WakeEventReceived.Inc()
}

// ObserveWakeLatencySeconds records mobile→host wake delivery latency.
// Negative or zero values are dropped (mobile clock skew or missing
// created_at).
func (m *Metrics) ObserveWakeLatencySeconds(seconds float64) {
	if m == nil || seconds <= 0 {
		return
	}
	m.WakeEventLatency.Observe(seconds)
}

// ObserveWSOpenSeconds records the host's signaling WS lifetime.
func (m *Metrics) ObserveWSOpenSeconds(seconds float64) {
	if m == nil || seconds < 0 {
		return
	}
	m.SignalingWSOpen.Observe(seconds)
}

// ObserveHeartbeat records a heartbeat outcome.
func (m *Metrics) ObserveHeartbeat(success bool) {
	if m == nil {
		return
	}
	label := "failure"
	if success {
		label = "success"
	}
	m.Heartbeat.WithLabelValues(label).Inc()
}

// ObserveRtdbReconnect records one SSE stream reconnect.
func (m *Metrics) ObserveRtdbReconnect() {
	if m == nil {
		return
	}
	m.RtdbListenerReconnect.Inc()
}

// SetRtdbListenerActive sets the SSE-connected gauge.
func (m *Metrics) SetRtdbListenerActive(active bool) {
	if m == nil {
		return
	}
	if active {
		m.RtdbListenerActive.Set(1)
	} else {
		m.RtdbListenerActive.Set(0)
	}
}
