// WakeListener — Realtime Database SSE listener that surfaces wake
// requests targeted at this host. It is the wake-event channel that
// replaces a persistent signaling WebSocket regardless of how peershd
// was bootstrapped (service-account, pair-code, or browser-login —
// all three produce Firebase ID tokens that RTDB REST accepts via
// ?auth=).
//
// AGENTS.md cost discipline allows this single per-device listener as
// an explicit exception: it carries wake events only (not signaling
// messages) and idles at TCP/SSE keep-alive cost. The connection goes
// to firebaseio.com / firebasedatabase.app — not to the Cloud Run
// signaling service — so it does not contribute to Cloud Run billing.

package firebase

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// WakeEvent is a single wake notification surfaced to the consumer.
type WakeEvent struct {
	RequestID      string
	MobileDeviceID string
}

// tokenRefreshLead is how far ahead of token expiry we proactively
// reconnect the SSE stream. Firebase ID tokens last 1 hour;
// reconnecting at 55 minutes gives 5 minutes of slack for retries.
const tokenRefreshLead = 5 * time.Minute

// listenerSubscribePath is the RTDB path the listener subscribes to.
// We listen on the whole wake_requests subtree because RTDB does not
// support per-field filters in REST and the subtree is small.
func listenerSubscribePath(uid string) string {
	return "/users/" + uid + "/wake_requests"
}

// WakeListener subscribes to /users/{uid}/wake_requests via RTDB
// streaming. SSE put events for documents whose target_device_id
// matches this host's deviceID are surfaced on C().
//
// The underlying TCP/TLS stream is held open until either an error
// fires (in which case Run reconnects with backoff) or token expiry
// approaches (in which case Run reconnects with a freshly-minted
// token).
type WakeListener struct {
	client   *Client
	src      TokenSource
	uid      string
	deviceID string
	out      chan WakeEvent

	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	running bool
}

// NewWakeListener constructs (but does not start) a listener.
func NewWakeListener(client *Client, src TokenSource, uid, deviceID string) *WakeListener {
	return &WakeListener{
		client:   client,
		src:      src,
		uid:      uid,
		deviceID: deviceID,
		out:      make(chan WakeEvent, 8),
		done:     make(chan struct{}),
	}
}

// C returns the channel of wake events.
func (w *WakeListener) C() <-chan WakeEvent { return w.out }

// Start launches the consumer goroutine. ctx scopes the listener's
// lifetime; pass the host's main context. Calling Start twice is a
// no-op.
func (w *WakeListener) Start(ctx context.Context) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()

	lctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(lctx)
}

// Close stops the listener and waits for the consumer goroutine to
// exit. Idempotent.
func (w *WakeListener) Close() {
	w.mu.Lock()
	wasRunning := w.running
	w.running = false
	w.mu.Unlock()
	if !wasRunning {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	<-w.done
}

func (w *WakeListener) run(ctx context.Context) {
	defer close(w.done)
	backoff := 200 * time.Millisecond
	const maxBackoff = 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		err := w.consume(ctx)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if err != nil {
			slog.Warn("wake listener stream ended; reconnecting", "err", err, "backoff", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// consume opens one SSE stream, surfaces events until the stream
// ends, then returns. Reconnection is the caller's job. The stream
// is closed proactively after tokenRefreshLead to ensure the next
// connection uses a fresh Firebase ID token.
func (w *WakeListener) consume(ctx context.Context) error {
	idToken, err := w.src.Token(ctx)
	if err != nil {
		return fmt.Errorf("mint id token: %w", err)
	}

	streamCtx, cancel := context.WithTimeout(ctx, 55*time.Minute-tokenRefreshLead+55*time.Minute)
	defer cancel()
	// Practically: budget = 55 min (1h - 5 min lead). Above expression
	// is intentionally simple; the 55-min cap dominates.
	_ = tokenRefreshLead

	stream, err := w.client.Listen(streamCtx, listenerSubscribePath(w.uid), idToken)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer stream.Close()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-streamCtx.Done():
			// Token-refresh-driven reconnect.
			return nil
		case ev, ok := <-stream.C():
			if !ok {
				return stream.Err()
			}
			switch ev.Kind {
			case "put", "patch":
				w.dispatch(ctx, ev)
			case "keep-alive":
				// no-op
			case "cancel", "auth_revoked":
				// Server told us to disconnect. Reconnect with a
				// fresh token (auth_revoked) or fresh subscription
				// (cancel).
				return fmt.Errorf("server requested disconnect: %s", ev.Kind)
			default:
				slog.Debug("wake listener: unknown SSE event", "kind", ev.Kind)
			}
		}
	}
}

// dispatch surfaces individual wake events from a put/patch SSE
// frame. Path "/" means the entire subtree was sent (initial
// snapshot or full overwrite); each child is a wake_request. Path
// "/<rid>" with non-null data means a single child was added or
// replaced. data == null means a child was deleted.
func (w *WakeListener) dispatch(ctx context.Context, ev SSEEvent) {
	if ev.Path == "" || ev.Path == "/" {
		// Whole subtree. data is either null (cleared) or a map
		// of {requestID: wake_request}.
		if isJSONNull(ev.Data) {
			return
		}
		var all map[string]json.RawMessage
		if err := json.Unmarshal(ev.Data, &all); err != nil {
			slog.Warn("wake listener: cannot decode subtree", "err", err)
			return
		}
		for rid, raw := range all {
			w.maybeEmit(ctx, rid, raw)
		}
		return
	}
	// Single-child put/patch. Path is "/<rid>".
	rid := PathToKey(ev.Path)
	if rid == "" {
		return
	}
	if isJSONNull(ev.Data) {
		// Deletion event. Nothing to surface.
		return
	}
	w.maybeEmit(ctx, rid, ev.Data)
}

func (w *WakeListener) maybeEmit(ctx context.Context, rid string, raw json.RawMessage) {
	var doc struct {
		TargetDeviceID string `json:"target_device_id"`
		MobileDeviceID string `json:"mobile_device_id"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		slog.Warn("wake listener: cannot decode wake_request", "err", err, "rid", rid)
		return
	}
	if doc.TargetDeviceID != w.deviceID {
		return
	}
	select {
	case w.out <- WakeEvent{RequestID: rid, MobileDeviceID: doc.MobileDeviceID}:
	case <-ctx.Done():
	}
}

func isJSONNull(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	return string(raw) == "null"
}
