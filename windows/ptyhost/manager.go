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
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"sync"
	"time"

	"github.com/peersh/peersh/windows/session"
)

// IdleTimeout is how long a detached Session is kept alive waiting for
// a reattach. After IdleTimeout, the sweeper closes it. 24h matches the
// pwsh.SessionManager default and lets a phone client survive a full
// day of OS backgrounding without losing its shell.
const IdleTimeout = 24 * time.Hour

// RingBufferSize is the per-Session scrollback cap, in bytes. 1 MiB
// holds several screens of history while still keeping the steady-state
// memory cost bounded for many parallel PTYs.
const RingBufferSize = 1 * 1024 * 1024

// ManagedHandle identifies a persisted Session. Stable across reattach.
// Marshalled as a 16-character base32 string for client storage.
type ManagedHandle string

// Owner partitions Manager entries so a client presenting one identity
// cannot see or attach to another client's PTYs. peershd uses the
// peer's mTLS-derived device_id (from peertls.PeerDeviceID) as the
// Owner — that id is stable across reconnects without any client
// upgrade, since the same long-lived ed25519 keypair drives every
// dial.
type Owner string

// Manager stores persisted Sessions and is process-global: a single
// Manager survives every QUIC reconnect, so a client that has saved a
// reattach handle can come back hours later (subject to IdleTimeout)
// and rebind to the same shell with replay. Entries are partitioned by
// Owner; cross-owner access is opaque (returns "unknown handle" rather
// than leaking existence).
type Manager struct {
	mu       sync.Mutex
	entries  map[ManagedHandle]*entry
	timeout  time.Duration
	ringSize int
	now      func() time.Time
	closed   bool
}

type entry struct {
	handle   ManagedHandle
	owner    Owner
	sess     *Session
	attached bool
	// attachGen monotonically increments on every Attach (and on the
	// initial Register) so a stale per-stream goroutine's Detach can
	// be distinguished from a fresh one's: if e.attachGen != gen, the
	// caller's slot has already been stolen by a newer Attach and the
	// Detach is a no-op. Pairs with Session.SetSink/ClearSink, which
	// uses the same swap-id pattern at the sink layer.
	attachGen uint64
	// kick is closed by Attach when a new client steals this entry, so
	// the previously-attached per-stream goroutine can wake up and
	// tear itself down promptly instead of waiting for QUIC idle.
	// Replaced on every Attach with a fresh chan; the previously-
	// attached caller holds the old chan and reacts to its closure.
	kick     chan struct{}
	lastSeen time.Time
	ring     *ringBuffer
	command  string // diagnostic: the command this PTY runs

	pumpDone chan struct{} // closed when the session-lifetime Pump returns
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

// AttachResult bundles everything servePTYStream needs to bind a new
// stream to a Session: the live Session, a snapshot of its scrollback
// ring buffer for replay, the per-attach generation (passed back to
// Detach so a stale goroutine can't clobber a fresh attach's state),
// and a kick channel that closes when a later Attach steals this
// entry so the current owner can tear itself down promptly.
type AttachResult struct {
	Session  *Session
	Replay   []byte
	Gen      uint64
	Kick     <-chan struct{}
}

// Register a freshly-opened Session under a fresh handle for the given
// owner and start its session-lifetime Pump goroutine. The ring buffer
// starts empty. Returned AttachResult is treated identically to a
// fresh Attach by the caller (servePTYStream).
func (m *Manager) Register(owner Owner, sess *Session, command string) (ManagedHandle, AttachResult) {
	h := newHandle()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", AttachResult{}
	}
	e := &entry{
		handle:    h,
		owner:     owner,
		sess:      sess,
		attached:  true,
		attachGen: 1,
		kick:      make(chan struct{}),
		lastSeen:  m.now(),
		ring:      newRingBuffer(m.ringSize),
		command:   command,
		pumpDone:  make(chan struct{}),
	}
	m.entries[h] = e
	m.mu.Unlock()

	// Mirror every chunk into the ring buffer; the snapshot is the
	// scrollback the next reattach replays. Captured here (not in the
	// per-stream sink) so a stream-less interval still records output.
	sess.SetRingAppend(func(b []byte) {
		e.ring.write(b)
	})

	go func() {
		defer close(e.pumpDone)
		// The Pump's ctx is never cancelled from here — we rely on
		// the child process's own exit (Watcher 1 inside Pump) or
		// Manager.Drop / Manager.Close calling sess.Close to unblock
		// Read. context.Background keeps that contract.
		sess.Pump(context.Background())
		// Pump returned (child exited, or the Manager closed sess);
		// remove the entry so a later reattach attempt fails cleanly.
		m.mu.Lock()
		if cur, ok := m.entries[h]; ok && cur == e {
			delete(m.entries, h)
		}
		m.mu.Unlock()
	}()

	return h, AttachResult{
		Session: sess,
		Replay:  nil, // fresh session, nothing to replay
		Gen:     1,
		Kick:    e.kick,
	}
}

// Attach binds owner to handle and returns the session, a snapshot of
// the ring buffer for replay, the new attach generation, and a kick
// channel that fires when a later Attach steals this entry.
//
// Owner mismatch is reported as "unknown handle" — the same error as
// a missing entry — so existence isn't leaked to a different session.
//
// Same-owner reattach while the entry is already-attached is a steal:
// the previous attach's kick channel is closed (signalling its
// per-stream goroutine to wind down), then attached/lastSeen/gen are
// updated for the new caller. The previous goroutine's Detach call
// becomes a no-op because the gen no longer matches.
func (m *Manager) Attach(owner Owner, h ManagedHandle) (AttachResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return AttachResult{}, errors.New("ptyhost: manager closed")
	}
	e, ok := m.entries[h]
	if !ok || e.owner != owner {
		return AttachResult{}, errors.New("ptyhost: unknown handle")
	}
	// Steal: signal the previously-attached goroutine to exit so it
	// doesn't keep reading from a stale wire stream.
	if e.kick != nil {
		close(e.kick)
	}
	e.kick = make(chan struct{})
	e.attached = true
	e.attachGen++
	e.lastSeen = m.now()
	return AttachResult{
		Session: e.sess,
		Replay:  e.ring.snapshot(),
		Gen:     e.attachGen,
		Kick:    e.kick,
	}, nil
}

// Detach marks the Session as no longer bound IF gen still matches the
// entry's current attachGen. Stale goroutines (whose attach was stolen
// by a newer Attach) silently no-op. Owner mismatch is also a no-op.
func (m *Manager) Detach(owner Owner, h ManagedHandle, gen uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[h]
	if !ok || e.owner != owner || e.attachGen != gen {
		return
	}
	e.attached = false
	e.lastSeen = m.now()
	if e.kick != nil {
		close(e.kick)
		e.kick = nil
	}
}

// Drop removes a Session immediately, closing the underlying PTY. Used
// when the client explicitly closes a PTY (vs. just disconnecting).
// Also wakes any currently-attached per-stream goroutine via kick.
// Owner mismatch is a silent no-op.
func (m *Manager) Drop(owner Owner, h ManagedHandle) {
	m.mu.Lock()
	e, ok := m.entries[h]
	if ok && e.owner == owner {
		delete(m.entries, h)
		if e.kick != nil {
			close(e.kick)
			e.kick = nil
		}
	} else {
		ok = false
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

// Append writes raw bytes directly into the entry's ring buffer.
// Retained as a test-only convenience: production code lets the
// Pump goroutine populate the ring via SetRingAppend. Owner mismatch
// is a silent no-op.
func (m *Manager) Append(owner Owner, h ManagedHandle, data []byte) {
	if len(data) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok && e.owner == owner {
		e.ring.write(data)
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

// List returns the persisted PTYs owned by owner. Sorted by LastSeenMs
// desc (most-recently-active first) for a stable client UI.
func (m *Manager) List(owner Owner) []Listing {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Listing, 0, len(m.entries))
	for _, e := range m.entries {
		if e.owner != owner {
			continue
		}
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
// even if it's currently detached. Owner mismatch returns "".
func (m *Manager) CWDOf(owner Owner, h ManagedHandle) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok && e.owner == owner {
		return e.sess.CWD()
	}
	return ""
}

// Get returns the live Session for h or (nil, false) if absent or not
// owned by owner. Does NOT mark as attached; callers that want exclusive
// ownership should use Attach instead.
func (m *Manager) Get(owner Owner, h ManagedHandle) (*Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.entries[h]; ok && e.owner == owner {
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
