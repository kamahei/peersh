// PTY persistence layer.
//
// Each Session is owned by a Manager that keeps the underlying ConPTY
// (and its child process) alive past the lifetime of the QUIC stream
// that opened it, so a client that reconnects to the same peersh-host
// can rebind to the same shell — including its scrollback — instead of
// losing context every time the network blips.
//
// Two pieces of long-term state per persisted Session:
//
//   - the ptyhost.Session itself (child process, ConPTY handles)
//   - a fixed-size ring buffer of recently-emitted output bytes
//
// The ring buffer cap (RingBufferSize) is small enough that idle PTYs
// never grow unboundedly, large enough that a typical reattach replays
// at least the last full screen plus a bit of history.
//
// Lifetime rules:
//
//   - Active: a stream is currently bound to the Session via Attach.
//     The ring buffer fills as bytes flow.
//   - Detached: the bound stream closed but the TTL hasn't elapsed yet.
//     The Manager's sweeper will close + drop after IdleTimeout.
//   - Reattached: a fresh stream calls Attach with the same reattach
//     handle; replay = the ring buffer's snapshot.

package ptyhost

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"sync"
	"time"

	"github.com/peersh/peersh/windows/session"
)

// IdleTimeout is how long a detached Session is kept alive waiting for
// a reattach. After IdleTimeout, the sweeper closes it.
const IdleTimeout = 30 * time.Minute

// RingBufferSize is the per-Session scrollback cap, in bytes. 256 KiB
// is generous for a single screen plus a handful of recent commands and
// keeps the steady-state memory cost bounded for many parallel PTYs.
const RingBufferSize = 256 * 1024

// ManagedHandle identifies a persisted Session. Stable across reattach.
// Marshalled as a 16-character base32 string for client storage.
type ManagedHandle string

// Manager stores persisted Sessions. It is per-connection in the current
// peersh design — each QUIC connection holds its own Manager because we
// have no way to authenticate a *different* client wanting to attach to
// someone else's PTYs.
type Manager struct {
	mu        sync.Mutex
	entries   map[ManagedHandle]*entry
	timeout   time.Duration
	ringSize  int
	now       func() time.Time
	closed    bool
}

type entry struct {
	handle    ManagedHandle
	sess      *Session
	attached  bool
	lastSeen  time.Time
	ring      *ringBuffer
	command   string // diagnostic: the command this PTY runs
}

// NewManager returns a Manager with the default IdleTimeout and
// RingBufferSize. SetIdleTimeout / SetRingBufferSize override before
// any Attach.
func NewManager() *Manager {
	return &Manager{
		entries:  make(map[ManagedHandle]*entry),
		timeout:  IdleTimeout,
		ringSize: RingBufferSize,
		now:      time.Now,
	}
}

// SetIdleTimeout overrides the default for tests.
func (m *Manager) SetIdleTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.timeout = d
}

// Register a freshly-opened Session under a fresh handle. The ring
// buffer starts empty.
func (m *Manager) Register(sess *Session, command string) ManagedHandle {
	h := newHandle()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ""
	}
	m.entries[h] = &entry{
		handle:   h,
		sess:     sess,
		attached: true,
		lastSeen: m.now(),
		ring:     newRingBuffer(m.ringSize),
		command:  command,
	}
	return h
}

// Attach marks the Session bound to handle as active and returns it
// along with the replay snapshot. attached=true means another stream is
// already bound — the caller should typically refuse the reattach
// rather than racing with the existing client.
func (m *Manager) Attach(h ManagedHandle) (*Session, []byte, bool /*alreadyAttached*/, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, nil, false, errors.New("ptyhost: manager closed")
	}
	e, ok := m.entries[h]
	if !ok {
		return nil, nil, false, errors.New("ptyhost: unknown handle")
	}
	if e.attached {
		return e.sess, e.ring.snapshot(), true, nil
	}
	e.attached = true
	e.lastSeen = m.now()
	return e.sess, e.ring.snapshot(), false, nil
}

// Detach marks the Session as no longer bound. The TTL begins now.
func (m *Manager) Detach(h ManagedHandle) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok {
		e.attached = false
		e.lastSeen = m.now()
	}
}

// Append feeds bytes into the Session's ring buffer. Called from the
// Pump loop alongside the wire-frame send so the replay reflects what
// the client just saw.
func (m *Manager) Append(h ManagedHandle, data []byte) {
	if len(data) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok {
		e.ring.write(data)
	}
}

// Drop removes a Session immediately, closing the underlying PTY. Used
// when the client explicitly closes a PTY (vs. just disconnecting).
func (m *Manager) Drop(h ManagedHandle) {
	m.mu.Lock()
	e, ok := m.entries[h]
	if ok {
		delete(m.entries, h)
	}
	m.mu.Unlock()
	if ok && e != nil {
		_ = e.sess.Close()
	}
}

// Sweep evicts Sessions whose TTL has elapsed. Returns the count.
func (m *Manager) Sweep() int {
	m.mu.Lock()
	now := m.now()
	expired := make([]*entry, 0)
	for h, e := range m.entries {
		if !e.attached && now.Sub(e.lastSeen) > m.timeout {
			expired = append(expired, e)
			delete(m.entries, h)
		}
	}
	m.mu.Unlock()
	for _, e := range expired {
		_ = e.sess.Close()
	}
	return len(expired)
}

// Close drops every entry, closing the underlying Sessions.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	entries := m.entries
	m.entries = nil
	m.mu.Unlock()
	for _, e := range entries {
		_ = e.sess.Close()
	}
}

// Listing is the snapshot a client gets via ListPTYs.
type Listing struct {
	Handle      ManagedHandle
	Command     string
	Attached    bool
	CWD         string
	LastSeenMs  int64
}

// List returns the current persisted PTYs. Sorted by LastSeenMs desc
// (most-recently-active first) for a stable client UI.
func (m *Manager) List() []Listing {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Listing, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, Listing{
			Handle:     e.handle,
			Command:    e.command,
			Attached:   e.attached,
			CWD:        e.sess.CWD(),
			LastSeenMs: e.lastSeen.UnixMilli(),
		})
	}
	// stable order — most recent first
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].LastSeenMs > out[i].LastSeenMs {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// CWDOf returns the most recent CWD the host observed for this PTY,
// even if it's currently detached.
func (m *Manager) CWDOf(h ManagedHandle) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok {
		return e.sess.CWD()
	}
	return ""
}

// Get returns the live Session for h or (nil, false) if absent. Does
// NOT mark as attached; callers that want exclusive ownership should
// use Attach instead.
func (m *Manager) Get(h ManagedHandle) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok {
		return e.sess, true
	}
	return nil, false
}

// --- internals -----------------------------------------------------------

type ringBuffer struct {
	mu   sync.Mutex
	buf  []byte
	cap  int
	head int  // next write index
	full bool // true once buf has wrapped at least once
}

// newRingBuffer pre-allocates the entire backing slice so steady-state
// writes don't churn allocations. Tests pass a small cap to make
// the eviction behaviour observable.
func newRingBuffer(cap int) *ringBuffer {
	return &ringBuffer{cap: cap, buf: make([]byte, 0, cap)}
}

// write appends p, dropping oldest bytes if necessary so the total
// stored never exceeds cap.
func (r *ringBuffer) write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cap <= 0 {
		return
	}
	// Easy path: fits in remaining capacity.
	if !r.full && len(r.buf)+len(p) <= r.cap {
		r.buf = append(r.buf, p...)
		r.head = len(r.buf)
		if r.head == r.cap {
			r.full = true
		}
		return
	}
	// We're past or about to pass cap; switch to true ring.
	if !r.full {
		// First time we cross the boundary. Backfill to cap.
		need := r.cap - len(r.buf)
		if need > len(p) {
			r.buf = append(r.buf, p...)
			r.head = len(r.buf)
			return
		}
		r.buf = append(r.buf, p[:need]...)
		p = p[need:]
		r.head = 0
		r.full = true
	}
	// Now r.buf is exactly r.cap long; write p across the head, wrapping.
	if len(p) >= r.cap {
		// p is bigger than the buffer; only its tail survives.
		copy(r.buf, p[len(p)-r.cap:])
		r.head = 0
		return
	}
	tail := r.cap - r.head
	if len(p) <= tail {
		copy(r.buf[r.head:r.head+len(p)], p)
		r.head += len(p)
		if r.head == r.cap {
			r.head = 0
		}
		return
	}
	copy(r.buf[r.head:], p[:tail])
	copy(r.buf, p[tail:])
	r.head = len(p) - tail
}

// snapshot returns a fresh slice of the current buffer contents in
// chronological order (oldest first). Safe to retain after the call.
func (r *ringBuffer) snapshot() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]byte, len(r.buf))
		copy(out, r.buf)
		return out
	}
	out := make([]byte, r.cap)
	copy(out, r.buf[r.head:])
	copy(out[r.cap-r.head:], r.buf[:r.head])
	return out
}

// newHandle returns a 16-character base32 random identifier matching
// the rest of peersh's id format.
func newHandle() ManagedHandle {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// In the unlikely event of randomness failure, fall back to a
		// timestamp-based id so the function still returns *something*
		// usable rather than an empty string.
		now := time.Now().UnixNano()
		for i := 0; i < 10; i++ {
			b[i] = byte(now >> (i * 8))
		}
	}
	return ManagedHandle(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

// (compile-time check that we still link to the session package; when
// Phase 6b Tier 2 ships, the Manager will start exposing per-PTY cwd
// snapshots through the same pipe used by Phase 8 Tier 2's file API.)
var _ = session.NewCWDTracker
