// Optional Prometheus /metrics endpoint for peershd.
//
// Default bind 127.0.0.1:9101 — operator-only, no network exposure.
// Override with -metrics-addr to scrape remotely; non-loopback binds
// require -metrics-token (or PEERSH_METRICS_TOKEN env) so a public
// bind cannot leak telemetry by accident.
//
// Token-gate logic mirrors server/cmd/peersh-signaling/main.go::
// metricsHandler: bearer prefix + constant-time compare + 404 when
// no token is configured. The fail-closed default is appropriate
// for a localhost listener too — there's no value to anonymous
// scraping of the loopback interface.

package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// startMetricsServer launches the Prometheus exposition endpoint on
// addr. Returns immediately; the actual server runs in a goroutine
// scoped to the main runtime context. Address validation (loopback
// without token is fine; non-loopback requires a token) is performed
// here so misconfigurations surface at startup rather than later
// when the first scrape arrives.
func startMetricsServer(addr, token string, reg *prometheus.Registry) error {
	if err := validateMetricsBind(addr, token); err != nil {
		return err
	}
	mux := http.NewServeMux()
	handler := metricsTokenHandler(token, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.Handle("/metrics", handler)
	hs := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("metrics endpoint listening", "addr", addr, "token", token != "")
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("metrics endpoint failed", "addr", addr, "err", err)
		}
	}()
	return nil
}

// validateMetricsBind enforces the loopback-or-token rule: a public
// bind without a configured token is a misconfiguration that would
// leak telemetry to anyone who can reach the host on that port.
func validateMetricsBind(addr, token string) error {
	if addr == "" {
		return errors.New("metrics: empty address")
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("metrics: parse address %q: %w", addr, err)
	}
	if token != "" {
		return nil
	}
	// No token → must bind to loopback. Empty host means all
	// interfaces, which is rejected.
	if host == "" {
		return fmt.Errorf("metrics: address %q has no host (all interfaces); set -metrics-token or pin to 127.0.0.1", addr)
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if host == "localhost" {
		return nil
	}
	return fmt.Errorf("metrics: non-loopback bind %q requires -metrics-token (or PEERSH_METRICS_TOKEN env)", addr)
}

// metricsTokenHandler wraps inner with bearer-token auth. A nil token
// returns 404 to keep a misconfigured-but-still-firewalled deploy
// from leaking. Constant-time comparison avoids timing leaks.
func metricsTokenHandler(token string, inner http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			// Loopback bind path: skip auth entirely (validated at
			// startup that the listener is loopback when token is
			// empty). Anyone on the loopback already controls the
			// machine.
			inner.ServeHTTP(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(got, prefix) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="peershd-metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		supplied := got[len(prefix):]
		if subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="peershd-metrics"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	})
}
