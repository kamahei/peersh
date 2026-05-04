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
//   - Active: at least one stream is currently bound via Attach. The
//     ring buffer fills as bytes flow and Pump fans each chunk out to
//     every attached sink.
//   - Detached: every previously-bound stream has called Detach but
//     the TTL hasn't elapsed yet. Pump keeps running and the ring
//     buffer keeps recording so a future Attach can replay.
//     Manager.Sweep closes + drops the entry after IdleTimeout.
//   - Multi-attach: more than one Attach is live at once. All bound
//     sinks see the same byte stream live and all of them can write
//     to the child via Session.Write (input chunks are serialised so
//     individual writes don't interleave mid-byte).

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

// IdleTimeout is how long a fully-detached Session is kept alive
// waiting for a reattach. After IdleTimeout, the sweeper closes it.
// 24h matches the pwsh.SessionManager default and lets a phone client
// survive a full day of OS backgrounding without losing its shell.
const IdleTimeout = 24 * time.Hour

// RingBufferSize is the per-Session scrollback cap, in bytes. 1 MiB
// holds several screens of history while still keeping the steady-state
// memory cost bounded for many parallel PTYs.
const RingBufferSize = 1 * 1024 * 1024

// ManagedHandle identifies a persisted Session. Stable across reattach.
// Marshalled as a 16-character base32 string for client storage.
type ManagedHandle string

// Owner partitions Manager entries so a client presenting one identity
// cannot see or attach to another's PTYs. peershd uses the
// authenticated user_id (from PSK Register or Firebase sign-in) so all
// of one operator's devices — Windows host, mobile app, dev CLI —
// share a single bucket and can hand off shells to each other. The
// signaling server already same-user-routes Connect frames, so any
// peer that reaches the host's QUIC accept loop is, by construction,
// a device of the host's own user.
type Owner string

// AttachToken bundles the bookkeeping a caller needs to undo an
// Attach. Pass it back to Detach when the stream closes; Manager
// handles the per-sink removal and attach-count decrement atomically.
type AttachToken struct {
	Handle   ManagedHandle
	SinkID   SinkToken
	owner    Owner // captured here so Detach doesn't need it as a separate arg
}

// AttachResult bundles everything servePTYStream needs to bind a new
// stream to a Session: the live Session, a snapshot of its scrollback
// ring buffer for replay, and an AttachToken to pass to Detach when
// the stream closes.
type AttachResult struct {
	Session *Session
	Replay  []byte
	Token   AttachToken
}

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
	handle      ManagedHandle
	owner       Owner
	sess        *Session
	attachCount int
	lastSeen    time.Time
	ring        *ringBuffer
	command     string // diagnostic: the command this PTY runs

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

// Register a freshly-opened Session under a fresh handle for the given
// owner, install the ring-mirroring callback, optionally attach the
// caller's sink, and start the session-lifetime Pump goroutine. The
// ring buffer starts empty; sink (if non-nil) is added atomically with
// that empty snapshot, so the caller observes the same lifetime
// guarantees as a fresh Attach to a brand new entry.
func (m *Manager) Register(owner Owner, sess *Session, command string, sink SinkFunc) (ManagedHandle, AttachResult) {
	h := newHandle()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", AttachResult{}
	}
	e := &entry{
		handle:   h,
		owner:    owner,
		sess:     sess,
		lastSeen: m.now(),
		ring:     newRingBuffer(m.ringSize),
		command:  command,
		pumpDone: make(chan struct{}),
	}
	m.entries[h] = e
	m.mu.Unlock()

	// Mirror every chunk into the ring buffer; the snapshot is the
	// scrollback the next reattach replays. Captured here (not in any
	// per-stream sink) so output keeps recording even when no client
	// is attached.
	sess.SetRingAppend(func(b []byte) {
		e.ring.write(b)
	})

	var (
		token SinkToken
	)
	if sink != nil {
		token = sess.AddSink(sink)
		m.mu.Lock()
		e.attachCount++
		e.lastSeen = m.now()
		m.mu.Unlock()
	}

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
		Token:   AttachToken{Handle: h, SinkID: token, owner: owner},
	}
}

// Attach binds owner to handle, atomically captures the current
// scrollback for replay, and (if sink is non-nil) installs sink so the
// caller starts receiving live frames immediately after replay. Owner
// mismatch is reported as "unknown handle" — the same error as a
// missing entry — so existence isn't leaked across owners.
//
// Multi-attach: a successful Attach NEVER displaces previously-bound
// streams. All attached sinks share the same byte stream live and the
// same input channel. Use Drop to terminate a session entirely.
func (m *Manager) Attach(owner Owner, h ManagedHandle, sink SinkFunc) (AttachResult, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return AttachResult{}, errors.New("ptyhost: manager closed")
	}
	e, ok := m.entries[h]
	if !ok || e.owner != owner {
		m.mu.Unlock()
		return AttachResult{}, errors.New("ptyhost: unknown handle")
	}
	m.mu.Unlock()

	var (
		token SinkToken
		snap  []byte
	)
	if sink != nil {
		token, snap = e.sess.AddSinkWithSnapshot(e.ring.snapshot, sink)
	} else {
		// Snapshot-only attach is occasionally useful (e.g. a client
		// that wants a one-off scrollback dump without a live tail).
		snap = e.ring.snapshot()
	}

	m.mu.Lock()
	e.attachCount++
	e.lastSeen = m.now()
	m.mu.Unlock()

	return AttachResult{
		Session: e.sess,
		Replay:  snap,
		Token:   AttachToken{Handle: h, SinkID: token, owner: owner},
	}, nil
}

// Detach undoes an Attach (or the implicit attach inside Register):
// removes the per-sink registration from the Session and decrements
// the entry's attach count. When the count hits zero the lastSeen
// clock starts so Sweep can reap idle sessions. Owner mismatch is a
// silent no-op (defence-in-depth; the Token already carries the
// correct owner so this should not normally happen).
func (m *Manager) Detach(t AttachToken) {
	m.mu.Lock()
	e, ok := m.entries[t.Handle]
	if !ok || e.owner != t.owner {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	if t.SinkID != 0 {
		e.sess.RemoveSink(t.SinkID)
	}

	m.mu.Lock()
	if e.attachCount > 0 {
		e.attachCount--
	}
	if e.attachCount == 0 {
		e.lastSeen = m.now()
	}
	m.mu.Unlock()
}

// Drop removes a Session immediately, closing the underlying PTY and
// surfacing PTYExit to whatever sinks remain. Used when the client
// explicitly closes a PTY (vs. just disconnecting). Owner mismatch is
// a silent no-op.
func (m *Manager) Drop(owner Owner, h ManagedHandle) {
	m.mu.Lock()
	e, ok := m.entries[h]
	if ok && e.owner == owner {
		delete(m.entries, h)
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
		if e.attachCount == 0 && now.Sub(e.lastSeen) > m.timeout {
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
	e, ok := m.entries[h]
	m.mu.Unlock()
	if !ok || e.owner != owner {
		return
	}
	e.ring.write(data)
}

// Listing is the snapshot a client gets via ListPTYs.
type Listing struct {
	Handle        ManagedHandle
	Command       string
	AttachedCount uint32
	CWD           string
	LastSeenMs    int64
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
			Handle:        e.handle,
			Command:       e.command,
			AttachedCount: uint32(e.attachCount),
			CWD:           e.sess.CWD(),
			LastSeenMs:    e.lastSeen.UnixMilli(),
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
	e, ok := m.entries[h]
	m.mu.Unlock()
	if !ok || e.owner != owner {
		return ""
	}
	return e.sess.CWD()
}

// Get returns the live Session for h or (nil, false) if absent or not
// owned by owner. Does NOT register a sink; callers that want to
// receive frames should use Attach instead.
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
