package room

import (
	"context"
	"errors"
	"fmt"
	"sync"

	signalv1 "github.com/peersh/peersh/core/protocol/peersh/signal/v1"
)

// Conn is the subset of a WebSocket connection that the registry needs in
// order to deliver forwarded Connect frames. Implemented by server/ws.
type Conn interface {
	UserID() string
	DeviceID() string
	Send(ctx context.Context, frame *signalv1.Frame) error
}

// Errors surfaced by Forward.
var (
	ErrTargetUnknown      = errors.New("room: target device is not registered")
	ErrCrossUserForbidden = errors.New("room: target device belongs to a different user")
	ErrSenderEqualsTarget = errors.New("room: cannot connect to self")
)

// Registry tracks live connections keyed by (user_id, device_id) and forwards
// Connect frames between them.
type Registry struct {
	mu      sync.RWMutex
	devices map[string]map[string]Conn // user_id → device_id → Conn
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{devices: make(map[string]map[string]Conn)}
}

// Register places c in the registry. If a connection for the same
// (user_id, device_id) already exists, it is replaced (and the previous
// connection is left for the caller to close).
func (r *Registry) Register(c Conn) (replaced Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	devices, ok := r.devices[c.UserID()]
	if !ok {
		devices = make(map[string]Conn)
		r.devices[c.UserID()] = devices
	}
	if prev, exists := devices[c.DeviceID()]; exists {
		replaced = prev
	}
	devices[c.DeviceID()] = c
	return replaced
}

// Unregister removes c from the registry if it is the currently-registered
// connection for its (user_id, device_id). A no-op if it isn't.
func (r *Registry) Unregister(c Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	devices, ok := r.devices[c.UserID()]
	if !ok {
		return
	}
	if cur, exists := devices[c.DeviceID()]; exists && cur == c {
		delete(devices, c.DeviceID())
		if len(devices) == 0 {
			delete(r.devices, c.UserID())
		}
	}
}

// Forward delivers msg to the target device. The from_device_id field on the
// forwarded copy is set to fromConn.DeviceID() — clients may not spoof it.
//
// Returns ErrTargetUnknown if the target is not currently registered, or
// ErrCrossUserForbidden if the target belongs to a different user.
func (r *Registry) Forward(ctx context.Context, fromConn Conn, msg *signalv1.Connect) error {
	if fromConn.DeviceID() == msg.GetTargetDeviceId() {
		return ErrSenderEqualsTarget
	}
	r.mu.RLock()
	devices, ok := r.devices[fromConn.UserID()]
	if !ok {
		r.mu.RUnlock()
		return ErrTargetUnknown
	}
	target, ok := devices[msg.GetTargetDeviceId()]
	r.mu.RUnlock()
	if !ok {
		return ErrTargetUnknown
	}
	if target.UserID() != fromConn.UserID() {
		// Cannot happen given the lookup is scoped to fromConn.UserID(), but
		// kept for explicit invariant documentation.
		return ErrCrossUserForbidden
	}

	forwarded := &signalv1.Connect{
		TargetDeviceId: msg.GetTargetDeviceId(),
		FromDeviceId:   fromConn.DeviceID(),
		Candidates:     msg.GetCandidates(),
	}
	if err := target.Send(ctx, &signalv1.Frame{
		Body: &signalv1.Frame_Connect{Connect: forwarded},
	}); err != nil {
		return fmt.Errorf("room: deliver to %s: %w", msg.GetTargetDeviceId(), err)
	}
	return nil
}

// CountByUser returns the number of registered devices for a user. Used by
// tests and metrics.
func (r *Registry) CountByUser(userID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.devices[userID])
}
