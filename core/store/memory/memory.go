package memory

import (
	"context"
	"sync"

	"github.com/peersh/peersh/core/store"
)

// Store is the in-memory implementation of store.Store. It is safe for
// concurrent use by multiple goroutines.
type Store struct {
	mu       sync.RWMutex
	devices  map[string]store.Device
	sessions map[string]store.Session
}

// New returns an initialized in-memory store.
func New() *Store {
	return &Store{
		devices:  make(map[string]store.Device),
		sessions: make(map[string]store.Session),
	}
}

// PutDevice inserts or replaces a device by ID.
func (s *Store) PutDevice(_ context.Context, d store.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[d.ID] = d
	return nil
}

// GetDevice returns the device with the given ID. Returns store.ErrNotFound
// if no such device exists.
func (s *Store) GetDevice(_ context.Context, id string) (store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return store.Device{}, store.ErrNotFound
	}
	return d, nil
}

// ListDevicesByOwner returns all devices belonging to the given user. Order
// is unspecified.
func (s *Store) ListDevicesByOwner(_ context.Context, userID string) ([]store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Device
	for _, d := range s.devices {
		if d.OwnerUserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

// DeleteDevice removes a device by ID. Returns store.ErrNotFound if no such
// device exists.
func (s *Store) DeleteDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.devices, id)
	return nil
}

// PutSession inserts or replaces a session by ID.
func (s *Store) PutSession(_ context.Context, sess store.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return nil
}

// GetSession returns the session with the given ID. Returns store.ErrNotFound
// if no such session exists.
func (s *Store) GetSession(_ context.Context, id string) (store.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return sess, nil
}

// DeleteSession removes a session by ID. Returns store.ErrNotFound if no
// such session exists.
func (s *Store) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.sessions, id)
	return nil
}

// Close releases any resources held by the store. The in-memory implementation
// has none; Close is a no-op.
func (s *Store) Close() error { return nil }
