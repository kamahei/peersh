package pwsh

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"sync"
	"time"
)

// DefaultIdleTimeout is the duration a Session may sit idle (no client
// attached AND no Exec activity) before SessionManager evicts it when
// the client did not specify a preference.
//
// 24 hours lets a phone client survive a full day of OS backgrounding
// without re-pairing. Clients can opt into a shorter window (down to
// MinIdleTimeout) via ClientHello.idle_timeout_sec.
const DefaultIdleTimeout = 24 * time.Hour

// MinIdleTimeout / MaxIdleTimeout are the bounds the host clamps any
// client-supplied override to. The minimum keeps a single transient
// disconnect from instantly evicting an in-flight shell; the maximum
// prevents an unbounded leak if a client asks for "forever".
const (
	MinIdleTimeout = 1 * time.Minute
	MaxIdleTimeout = 7 * 24 * time.Hour
)

// ClampIdleTimeoutSec returns the per-session idle timeout to apply
// given a client-supplied seconds value. Zero falls back to the manager
// default; non-zero values are clamped into [MinIdleTimeout,
// MaxIdleTimeout].
func ClampIdleTimeoutSec(sec uint32) time.Duration {
	if sec == 0 {
		return 0
	}
	d := time.Duration(sec) * time.Second
	if d < MinIdleTimeout {
		return MinIdleTimeout
	}
	if d > MaxIdleTimeout {
		return MaxIdleTimeout
	}
	return d
}

// SessionManager keeps long-lived pwsh.Host instances around so a client
// reconnecting with the same session_id can resume where it left off
// (cwd, variables, in-flight prompt). It's safe for concurrent use.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*managedSession
	timeout  time.Duration
	now      func() time.Time

	closed bool
}

// NewSessionManager returns a SessionManager with the default idle
// timeout (DefaultIdleTimeout).
func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*managedSession),
		timeout:  DefaultIdleTimeout,
		now:      time.Now,
	}
}

// SetIdleTimeout overrides DefaultIdleTimeout. Must be called before any
// Attach.
func (m *SessionManager) SetIdleTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeout = d
}

// AttachOrCreate either returns the existing host for sessionID (and
// reattached = true) or starts a fresh pwsh.Host and assigns a new id.
// Empty sessionID always produces a fresh session.
//
// idleTimeoutSec is the client's preferred lifetime for the session
// when detached. 0 means "use the manager default". Non-zero values
// are clamped via ClampIdleTimeoutSec. The override is stored on the
// session and replaces any previous client preference on reattach.
//
// The caller MUST call Detach when its client connection ends so the
// idle timer can start.
func (m *SessionManager) AttachOrCreate(ctx context.Context, sessionID string, idleTimeoutSec uint32) (id string, host *Host, reattached bool, err error) {
	override := ClampIdleTimeoutSec(idleTimeoutSec)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", nil, false, errors.New("pwsh: session manager closed")
	}
	if sessionID != "" {
		if s, ok := m.sessions[sessionID]; ok && !s.expiredLocked(m.now(), m.effectiveTimeoutLocked(s)) {
			s.attached = true
			s.lastActivity = m.now()
			s.idleTimeout = override
			m.mu.Unlock()
			return sessionID, s.host, true, nil
		}
	}
	m.mu.Unlock()

	// Fresh session.
	h, err := Start(ctx)
	if err != nil {
		return "", nil, false, err
	}
	id = newSessionID()
	now := m.now()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = h.Close()
		return "", nil, false, errors.New("pwsh: session manager closed")
	}
	m.sessions[id] = &managedSession{
		host:         h,
		attached:     true,
		lastActivity: now,
		idleTimeout:  override,
	}
	m.mu.Unlock()
	return id, h, false, nil
}

// effectiveTimeoutLocked returns the idle timeout that should apply to
// the given session — its per-session override if set, else the
// manager-wide default. Caller must hold m.mu.
func (m *SessionManager) effectiveTimeoutLocked(s *managedSession) time.Duration {
	if s.idleTimeout > 0 {
		return s.idleTimeout
	}
	return m.timeout
}

// Detach marks the session as no longer attached and starts the idle
// timer. The host process keeps running.
func (m *SessionManager) Detach(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[sessionID]; ok {
		s.attached = false
		s.lastActivity = m.now()
	}
}

// Sweep evicts sessions whose idle window has elapsed. Returns the
// number of sessions evicted.
func (m *SessionManager) Sweep() int {
	m.mu.Lock()
	now := m.now()
	expired := make([]string, 0)
	for id, s := range m.sessions {
		if s.expiredLocked(now, m.effectiveTimeoutLocked(s)) {
			expired = append(expired, id)
		}
	}
	for _, id := range expired {
		s := m.sessions[id]
		delete(m.sessions, id)
		go func(s *managedSession) { _ = s.host.Close() }(s)
	}
	m.mu.Unlock()
	return len(expired)
}

// Run starts a periodic Sweep. Stops when ctx is cancelled.
func (m *SessionManager) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Sweep()
		}
	}
}

// Close evicts every session, closing each underlying pwsh.Host.
func (m *SessionManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	hosts := make([]*Host, 0, len(m.sessions))
	for _, s := range m.sessions {
		hosts = append(hosts, s.host)
	}
	m.sessions = nil
	m.mu.Unlock()
	for _, h := range hosts {
		_ = h.Close()
	}
	return nil
}

// CountSessions returns the current session count. Useful for metrics
// and tests.
func (m *SessionManager) CountSessions() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

// --- internals -------------------------------------------------------------

type managedSession struct {
	host         *Host
	attached     bool
	lastActivity time.Time
	// idleTimeout is the per-session override of the manager's default
	// idle timeout. Zero means "fall back to manager default". Set from
	// ClientHello.idle_timeout_sec on attach/create.
	idleTimeout time.Duration
}

func (s *managedSession) expiredLocked(now time.Time, timeout time.Duration) bool {
	if s.attached {
		return false
	}
	return now.Sub(s.lastActivity) > timeout
}

// newSessionID returns a 16-character base32 random identifier.
func newSessionID() string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Random failure here is exotic; fall back to a deterministic
		// timestamp-based id rather than crashing.
		return fmt.Sprintf("FALLBACK%010d", time.Now().UnixNano())
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])
}
