package ws

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics is the set of counters / gauges exposed by the signaling
// server. Construct via NewMetrics; Register exposes them on the given
// prometheus registry. Call from main.go and pass the result into
// Server via the Metrics field.
type Metrics struct {
	UpgradeAccepted prometheus.Counter
	UpgradeRejected *prometheus.CounterVec // rejected_reason in { "rate_limited" }

	RegisterAccepted prometheus.Counter
	RegisterRejected *prometheus.CounterVec // rejected_reason in { "auth", "rate_limit", "frame_shape" }

	ConnectsForwarded prometheus.Counter
	ConnectsRejected  *prometheus.CounterVec // reason in { "target_unknown", "cross_user_forbidden", "self_target", "rate_limit" }

	ActiveConnections prometheus.Gauge

	// IdleClosed counts connections the server tore down because the
	// client stopped sending frames within Server.IdleTimeout. A
	// non-zero rate hints at a misbehaving / frozen client (or a
	// timeout setting that's too aggressive).
	IdleClosed prometheus.Counter

	// SessionDuration observes how long each registered connection
	// stayed open (Register accepted → connection close). In v2-A
	// Firebase mode the expectation is < 20 s P95.
	SessionDuration prometheus.Histogram

	// RegisterToFirstConnect observes the time from Register accepted
	// to the first Connect frame on the same connection. Server-side
	// proxy for "how cold was the host" — a high P95 indicates the
	// mobile is racing ahead of the host's wake-up.
	RegisterToFirstConnect prometheus.Histogram
}

// NewMetrics builds the Prometheus collectors. NewMetrics does NOT
// register them; call Register afterwards (or pass the result through
// prometheus.MustRegister).
func NewMetrics() *Metrics {
	m := &Metrics{
		UpgradeAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peersh_ws_upgrade_accepted_total",
			Help: "WebSocket upgrades that completed successfully.",
		}),
		UpgradeRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "peersh_ws_upgrade_rejected_total",
			Help: "WebSocket upgrades rejected before the protocol started.",
		}, []string{"reason"}),
		RegisterAccepted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peersh_ws_register_accepted_total",
			Help: "Register frames that authenticated cleanly.",
		}),
		RegisterRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "peersh_ws_register_rejected_total",
			Help: "Register frames rejected after the WebSocket upgrade.",
		}, []string{"reason"}),
		ConnectsForwarded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peersh_ws_connect_forwarded_total",
			Help: "Connect frames the room registry routed to a peer.",
		}),
		ConnectsRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "peersh_ws_connect_rejected_total",
			Help: "Connect frames the room registry refused to route.",
		}, []string{"reason"}),
		ActiveConnections: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "peersh_ws_active_connections",
			Help: "Number of currently-registered WebSocket connections.",
		}),
		IdleClosed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "peersh_ws_idle_closed_total",
			Help: "Connections the server closed because no frame arrived within IdleTimeout.",
		}),
		SessionDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "peersh_ws_session_duration_seconds",
			Help:    "Per-connection WebSocket lifetime (Register accepted → close).",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 60, 300},
		}),
		RegisterToFirstConnect: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "peersh_ws_register_to_first_connect_seconds",
			Help:    "Time from Register accepted to the first Connect frame on the same connection.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 20},
		}),
	}
	return m
}

// Register adds every collector in m to the given Prometheus registry.
// Use the default registry by passing prometheus.DefaultRegisterer.
func (m *Metrics) Register(reg prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		m.UpgradeAccepted,
		m.UpgradeRejected,
		m.RegisterAccepted,
		m.RegisterRejected,
		m.ConnectsForwarded,
		m.ConnectsRejected,
		m.ActiveConnections,
		m.IdleClosed,
		m.SessionDuration,
		m.RegisterToFirstConnect,
	} {
		if err := reg.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// observeUpgradeAccepted is the noop-safe helper used by handler.go.
func (m *Metrics) observeUpgradeAccepted() {
	if m == nil {
		return
	}
	m.UpgradeAccepted.Inc()
	m.ActiveConnections.Inc()
}

func (m *Metrics) observeUpgradeRejected(reason string) {
	if m == nil {
		return
	}
	m.UpgradeRejected.WithLabelValues(reason).Inc()
}

func (m *Metrics) observeRegisterAccepted() {
	if m == nil {
		return
	}
	m.RegisterAccepted.Inc()
}

func (m *Metrics) observeRegisterRejected(reason string) {
	if m == nil {
		return
	}
	m.RegisterRejected.WithLabelValues(reason).Inc()
}

func (m *Metrics) observeConnectForwarded() {
	if m == nil {
		return
	}
	m.ConnectsForwarded.Inc()
}

func (m *Metrics) observeConnectRejected(reason string) {
	if m == nil {
		return
	}
	m.ConnectsRejected.WithLabelValues(reason).Inc()
}

func (m *Metrics) observeConnectionClosed() {
	if m == nil {
		return
	}
	m.ActiveConnections.Dec()
}

func (m *Metrics) observeConnectionIdleClosed() {
	if m == nil {
		return
	}
	m.IdleClosed.Inc()
}

func (m *Metrics) observeSessionDuration(seconds float64) {
	if m == nil || seconds < 0 {
		return
	}
	m.SessionDuration.Observe(seconds)
}

func (m *Metrics) observeRegisterToFirstConnect(seconds float64) {
	if m == nil || seconds < 0 {
		return
	}
	m.RegisterToFirstConnect.Observe(seconds)
}
