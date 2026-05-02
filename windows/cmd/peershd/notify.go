// Per-PTY notification state machine for v2-B.
//
// servePTYStream constructs one ptyNotifyState per opened PTY when
// Firebase mode is active. The state tracks:
//
//   - the latest PTYNotificationConfig the mobile sent (toggle
//     state, threshold, idle window, target mobile_device_id);
//   - command timing (lastInputAt — last typed input from mobile;
//     lastOutputAt — last byte produced by the child PTY;
//     commandRunning — true between an Input and the next fire);
//   - a per-instance CWDTracker that re-parses the PTY output
//     stream looking for OSC 9;9 prompt markers (independent of
//     ptyhost.Session's tracker — we don't want to plumb a callback
//     through that layer);
//   - cooldown to debounce rapid back-to-back fires.
//
// Two firing paths:
//
//   - OSC 9;9 detected on output → "prompt" notification when the
//     elapsed time since the most recent Input exceeds threshold.
//   - 1 Hz idle ticker → "idle" notification when output has been
//     silent for cfg.IdleSeconds while a command is still running
//     (heuristic for non-shell tools like Claude / Codex).
//
// Every receiver is nil-safe so the call sites in servePTYStream
// don't have to guard `if notifier != nil` everywhere.

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	fbpeershd "github.com/peersh/peersh/windows/firebase"
	"github.com/peersh/peersh/windows/session"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
)

// notifyCooldown is the minimum gap between consecutive notifications
// from the same PTY. Prevents an interactive shell that prints a new
// prompt every second from firing a notification per command.
const notifyCooldown = 5 * time.Second

// notifyDefaultThreshold is the assumed minimum command duration when
// the mobile sends a config without ThresholdSeconds set.
const notifyDefaultThreshold = 10 * time.Second

// ptyNotifyState owns all v2-B state for a single PTY stream.
// Receivers are nil-safe.
type ptyNotifyState struct {
	nctx  *notifyCtx
	ptyID int64
	log   *slog.Logger
	cwdt  *session.CWDTracker

	mu             sync.Mutex
	cfg            *v1.PTYNotificationConfig
	lastInputAt    time.Time
	lastOutputAt   time.Time
	commandRunning bool
	lastFiredAt    time.Time
}

func newPTYNotifyState(nctx *notifyCtx, ptyID int64, log *slog.Logger) *ptyNotifyState {
	return &ptyNotifyState{
		nctx:  nctx,
		ptyID: ptyID,
		log:   log.With("notify", "pty"),
		cwdt:  session.NewCWDTracker(),
	}
}

// setConfig replaces the active config with what the mobile just sent.
// A nil config — passed when the mobile flips the bell off — disables
// the feature on this PTY without dropping the per-PTY state.
func (s *ptyNotifyState) setConfig(cfg *v1.PTYNotificationConfig) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// onInput records that the mobile just sent keystrokes. We treat any
// input arrival as "a command is now running" — there's no clean
// boundary in a PTY stream between idle typing and a Return that
// kicks off a command, so we use the simpler approximation.
func (s *ptyNotifyState) onInput() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.lastInputAt = now
	s.commandRunning = true
}

// onOutput is called from the pump callback for every PTYData chunk.
// Records the activity timestamp, feeds the CWDTracker so OSC 9;9
// detection runs, and dispatches a notification asynchronously when
// the prompt-completion conditions are met.
func (s *ptyNotifyState) onOutput(ctx context.Context, data []byte) {
	if s == nil || len(data) == 0 {
		return
	}
	s.mu.Lock()
	s.lastOutputAt = time.Now()
	paths := s.cwdt.Feed(data)
	s.mu.Unlock()
	if len(paths) == 0 {
		return
	}
	s.maybeFire(ctx, "prompt")
}

// runIdleTicker drives the silence-detection heuristic for non-shell
// tools. Wakes once per second; cheap when the feature is disabled
// because maybeFire short-circuits on cfg.
func (s *ptyNotifyState) runIdleTicker(ctx context.Context) {
	if s == nil {
		return
	}
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.maybeFire(ctx, "idle")
		}
	}
}

// maybeFire evaluates whether to dispatch a notification right now.
// reason is "prompt" (OSC 9;9 hit) or "idle" (silence ticker).
func (s *ptyNotifyState) maybeFire(ctx context.Context, reason string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	cfg := s.cfg
	if cfg == nil || !cfg.GetEnabled() {
		s.mu.Unlock()
		return
	}
	if cfg.GetMobileDeviceId() == "" {
		// Mobile didn't supply its FCM-routing id; we can't reach
		// the right device anyway.
		s.mu.Unlock()
		return
	}
	if !s.commandRunning {
		s.mu.Unlock()
		return
	}
	now := time.Now()
	if !s.lastFiredAt.IsZero() && now.Sub(s.lastFiredAt) < notifyCooldown {
		s.mu.Unlock()
		return
	}

	threshold := time.Duration(cfg.GetThresholdSeconds()) * time.Second
	if threshold == 0 {
		threshold = notifyDefaultThreshold
	}

	switch reason {
	case "prompt":
		// elapsed since the user-typed Input that started this run.
		// Below threshold = trivial command (ls, etc.); skip.
		if now.Sub(s.lastInputAt) < threshold {
			s.mu.Unlock()
			return
		}
	case "idle":
		idleWindow := time.Duration(cfg.GetIdleSeconds()) * time.Second
		if idleWindow == 0 {
			s.mu.Unlock()
			return
		}
		if now.Sub(s.lastOutputAt) < idleWindow {
			s.mu.Unlock()
			return
		}
	default:
		s.mu.Unlock()
		return
	}

	// Capture everything the dispatch goroutine needs, then release
	// the lock. State updates happen here so concurrent maybeFire
	// calls debounce against the same lastFiredAt.
	s.lastFiredAt = now
	s.commandRunning = false
	tabLabel := cfg.GetTabLabel()
	mobileDeviceID := cfg.GetMobileDeviceId()
	durationSeconds := now.Sub(s.lastInputAt).Seconds()
	s.mu.Unlock()

	go s.dispatch(ctx, mobileDeviceID, tabLabel, reason, durationSeconds)
}

func (s *ptyNotifyState) dispatch(ctx context.Context, mobileDeviceID, tabLabel, reason string, durationSeconds float64) {
	body := s.formatBody(tabLabel, reason, durationSeconds)
	payload := fbpeershd.NotificationPayload{
		MobileDeviceID: mobileDeviceID,
		HostDeviceID:   s.nctx.hostDeviceID,
		Title:          "peersh: " + s.nctx.hostName,
		Body:           body,
		DeepLink: map[string]string{
			"host_device_id": s.nctx.hostDeviceID,
			"pty_id":         fmt.Sprintf("%d", s.ptyID),
			"tab_label":      tabLabel,
			"reason":         reason,
		},
	}
	if err := s.nctx.notifier.NotifyCommandReady(ctx, payload); err != nil {
		s.nctx.metrics.ObserveNotificationDispatchFailure(reason)
		s.log.Warn("notify dispatch failed", "err", err, "reason", reason, "pty_id", s.ptyID)
		return
	}
	s.nctx.metrics.ObserveNotificationDispatched(reason)
	s.log.Info("notify dispatched", "reason", reason, "pty_id", s.ptyID, "duration_s", fmt.Sprintf("%.1f", durationSeconds))
}

func (s *ptyNotifyState) formatBody(tabLabel, reason string, durationSeconds float64) string {
	if tabLabel == "" {
		tabLabel = fmt.Sprintf("pty %d", s.ptyID)
	}
	switch reason {
	case "prompt":
		return fmt.Sprintf("%s: command finished (%.1fs)", tabLabel, durationSeconds)
	case "idle":
		return fmt.Sprintf("%s: waiting for input (%.0fs idle)", tabLabel, durationSeconds)
	default:
		return tabLabel
	}
}
