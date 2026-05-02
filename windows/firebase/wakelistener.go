// WakeListener — Firestore snapshot listener that surfaces wake
// requests targeted at this host. It is the wake-event channel that
// replaces a persistent signaling WebSocket in service-account-mode
// peershd.
//
// AGENTS.md cost discipline allows this single per-device listener as
// an explicit exception: it carries wake events only (not signaling
// messages) and idles at gRPC keep-alive cost. One document read is
// charged per delivered wake.
//
// Pair-code mode hosts cannot use this listener (Go Firestore client
// requires service-account credentials, not Firebase ID tokens) and
// stay on the persistent-WS path.

package firebase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	fs "cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// WakeEvent is a single wake notification surfaced to the consumer.
type WakeEvent struct {
	RequestID      string
	MobileDeviceID string
}

// WakeListener subscribes to users/{uid}/wake_requests filtered for
// this host's deviceID and surfaces newly added documents on C().
// The underlying gRPC stream is reconnected automatically by the
// Firestore SDK; on permanent errors the listener resubscribes after
// a short backoff.
type WakeListener struct {
	client   *fs.Client
	uid      string
	deviceID string
	out      chan WakeEvent

	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	running bool
}

// NewWakeListener constructs (but does not start) a listener. Call
// Start to begin streaming snapshots; Close to tear it down.
func NewWakeListener(client *fs.Client, uid, deviceID string) *WakeListener {
	return &WakeListener{
		client:   client,
		uid:      uid,
		deviceID: deviceID,
		out:      make(chan WakeEvent, 8),
		done:     make(chan struct{}),
	}
}

// C returns the channel of wake events.
func (w *WakeListener) C() <-chan WakeEvent { return w.out }

// Start launches the snapshot consumer goroutine. ctx scopes the
// listener's lifetime; pass the host's main context. Calling Start
// twice is safe; the second call is a no-op.
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
	q := w.client.Collection("users").Doc(w.uid).
		Collection("wake_requests").
		Where("target_device_id", "==", w.deviceID)

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		err := w.consume(ctx, q)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if err != nil {
			slog.Warn("wake listener stream ended; reconnecting", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (w *WakeListener) consume(ctx context.Context, q fs.Query) error {
	it := q.Snapshots(ctx)
	defer it.Stop()
	for {
		snap, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("snapshots: %w", err)
		}
		for _, ch := range snap.Changes {
			if ch.Kind != fs.DocumentAdded {
				continue
			}
			data := ch.Doc.Data()
			// consumed is filtered client-side so we don't need a
			// Firestore composite index. The TTL policy on
			// expires_at also bounds the clutter.
			if asBool(data["consumed"]) {
				continue
			}
			ev := WakeEvent{
				RequestID:      ch.Doc.Ref.ID,
				MobileDeviceID: asString(data["mobile_device_id"]),
			}
			select {
			case w.out <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}
