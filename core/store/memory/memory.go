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
	users    map[string]store.User
	psks     map[string]store.PSKRecord            // user_id → record
	pairs    map[pairingKey]store.Pairing
}

type pairingKey struct {
	userID         string
	mobileDeviceID string
	hostDeviceID   string
}

// New returns an initialized in-memory store.
func New() *Store {
	return &Store{
		devices:  make(map[string]store.Device),
		sessions: make(map[string]store.Session),
		users:    make(map[string]store.User),
		psks:     make(map[string]store.PSKRecord),
		pairs:    make(map[pairingKey]store.Pairing),
	}
}

// --- Device ----------------------------------------------------------------

func (s *Store) PutDevice(_ context.Context, d store.Device) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.devices[d.ID] = d
	return nil
}

func (s *Store) GetDevice(_ context.Context, id string) (store.Device, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return store.Device{}, store.ErrNotFound
	}
	return d, nil
}

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

func (s *Store) DeleteDevice(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.devices, id)
	return nil
}

// --- Session ---------------------------------------------------------------

func (s *Store) PutSession(_ context.Context, sess store.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
	return nil
}

func (s *Store) GetSession(_ context.Context, id string) (store.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	if !ok {
		return store.Session{}, store.ErrNotFound
	}
	return sess, nil
}

func (s *Store) DeleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[id]; !ok {
		return store.ErrNotFound
	}
	delete(s.sessions, id)
	return nil
}

// --- User ------------------------------------------------------------------

func (s *Store) PutUser(_ context.Context, u store.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.users[u.ID] = u
	return nil
}

func (s *Store) GetUser(_ context.Context, id string) (store.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return u, nil
}

// --- PSKRecord -------------------------------------------------------------

func (s *Store) PutPSKRecord(_ context.Context, r store.PSKRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.psks[r.UserID] = r
	return nil
}

func (s *Store) GetPSKRecord(_ context.Context, userID string) (store.PSKRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.psks[userID]
	if !ok {
		return store.PSKRecord{}, store.ErrNotFound
	}
	return r, nil
}

func (s *Store) ListPSKRecords(_ context.Context) ([]store.PSKRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.PSKRecord, 0, len(s.psks))
	for _, r := range s.psks {
		out = append(out, r)
	}
	return out, nil
}

func (s *Store) DeletePSKRecord(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.psks[userID]; !ok {
		return store.ErrNotFound
	}
	delete(s.psks, userID)
	return nil
}

// --- Pairing ---------------------------------------------------------------

func (s *Store) PutPairing(_ context.Context, p store.Pairing) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairs[pairingKey{p.UserID, p.MobileDeviceID, p.HostDeviceID}] = p
	return nil
}

func (s *Store) GetPairing(_ context.Context, userID, mobileDeviceID, hostDeviceID string) (store.Pairing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.pairs[pairingKey{userID, mobileDeviceID, hostDeviceID}]
	if !ok {
		return store.Pairing{}, store.ErrNotFound
	}
	return p, nil
}

func (s *Store) ListPairingsByUser(_ context.Context, userID string) ([]store.Pairing, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []store.Pairing
	for k, p := range s.pairs {
		if k.userID == userID {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *Store) DeletePairing(_ context.Context, userID, mobileDeviceID, hostDeviceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pairingKey{userID, mobileDeviceID, hostDeviceID}
	if _, ok := s.pairs[k]; !ok {
		return store.ErrNotFound
	}
	delete(s.pairs, k)
	return nil
}

// Close releases any resources held by the store. The in-memory implementation
// has none; Close is a no-op.
func (s *Store) Close() error { return nil }
