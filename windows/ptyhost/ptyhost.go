// Package ptyhost wires a windows/pty.PTY to peersh's PTY protocol frames
// (peersh.v1.PTYInput / PTYResize / PTYData / PTYExit).
//
// Sessions persist across QUIC reconnects: a Session opened during one
// connection is held by the process-global ptyhost.Manager (see
// manager.go) and can be rebound by a later connection that presents
// the matching reattach handle plus the same peer device_id Owner.
// Manager.Sweep reaps detached Sessions whose IdleTimeout has elapsed.
//
// Pump is session-lifetime: Manager.Register starts one Pump goroutine
// per Session and the same goroutine runs until the child process
// exits (or the Manager evicts the entry). Output bytes flow into
// (a) the entry's ring buffer (always) and (b) the currently-installed
// sink (set by whichever stream is attached). When a new client
// reconnects and steals the attachment, it just installs a new sink;
// the old sink is dropped without closing the underlying PTY.
package ptyhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/windows/pty"
	"github.com/peersh/peersh/windows/session"
	"github.com/peersh/peersh/windows/shell"
)

// SinkFunc receives PTYData / PTYExit frames produced by a Session's
// Pump. Returning an error is harmless — Pump just drops the frame and
// keeps running (the next SetSink swap will provide a fresh sink).
type SinkFunc func(*v1.PTYFrame) error

// RingFunc is called for every PTYData payload before sink dispatch so
// the caller (Manager) can mirror the bytes into the per-entry
// scrollback ring buffer.
type RingFunc func([]byte)

// Session wraps a pty.PTY and exposes Write / Resize / Close. The
// session-lifetime Pump goroutine inside Manager drives the output
// direction; per-stream sinks are swapped via SetSink/ClearSink.
//
// Tier 2 addition: each session owns a CWDTracker that scans the
// child's output for OSC 9;9 prompt sequences. The tracker is fed
// transparently inside Pump so file-API callers (GetSession /
// ListSessionFiles / ReadSessionFile) can resolve paths against the
// shell's last-observed cwd.
type Session struct {
	p pty.PTY

	mu     sync.Mutex
	closed bool

	sinkMu     sync.RWMutex
	sink       SinkFunc
	sinkID     uint64 // monotonically incremented on each SetSink swap
	ringAppend RingFunc

	tracker *session.CWDTracker
	cwdMu   sync.RWMutex
	cwd     string
}

// Open spawns the requested command (or the default shell wrapper) under a
// pseudo-console of the given size.
//
// command empty / "auto": resolve via shell.Resolve("auto"); the returned
// args install peersh's OSC 9;9 prompt wrapper.
//
// command non-empty: spawn it verbatim with args. Useful for "claude" /
// "codex" — programs that already manage their own prompt; the OSC 9;9
// instrumentation is unhelpful there.
func Open(command string, args []string, cols, rows uint16) (*Session, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	exe := command
	finalArgs := args
	if command == "" || command == "auto" || command == "pwsh" || command == "powershell" || command == "cmd" {
		r, err := shell.Resolve(command)
		if err != nil {
			return nil, fmt.Errorf("ptyhost: resolve shell: %w", err)
		}
		exe = r.Path
		finalArgs = r.Args
		if len(args) > 0 {
			// Caller supplied extra args: append AFTER the wrapper args so
			// (e.g.) -File somescript.ps1 still composes with our prompt
			// wrapper. The wrapper already ends with -EncodedCommand <b64>;
			// PowerShell tolerates further switches after that.
			finalArgs = append(finalArgs, args...)
		}
	}

	p, err := pty.Spawn(exe, finalArgs, cols, rows)
	if err != nil {
		return nil, fmt.Errorf("ptyhost: spawn %q: %w", exe, err)
	}
	return &Session{p: p, tracker: session.NewCWDTracker()}, nil
}

// CWD returns the last current-working-directory observed in the child
// process's prompt output (via OSC 9;9 emitted by the shell wrapper).
// Returns empty until the first prompt has rendered. Safe for concurrent
// use.
func (s *Session) CWD() string {
	s.cwdMu.RLock()
	defer s.cwdMu.RUnlock()
	return s.cwd
}

// Write forwards user keystrokes / paste payloads to the child process.
func (s *Session) Write(data []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, errClosed
	}
	s.mu.Unlock()
	return s.p.Write(data)
}

// Resize tells the child the terminal grid dimensions changed.
func (s *Session) Resize(cols, rows uint16) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errClosed
	}
	s.mu.Unlock()
	return s.p.Resize(cols, rows)
}

// Close terminates the child process and releases the pseudo-console.
// Calling Close more than once is safe.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	return s.p.Close()
}

// SetSink installs sink as the current output destination, replacing
// any previously installed sink. Returns a swap id the caller passes
// to ClearSink so a stale tear-down can't clobber a fresh attach.
func (s *Session) SetSink(sink SinkFunc) uint64 {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	s.sinkID++
	s.sink = sink
	return s.sinkID
}

// ClearSink removes the sink that was installed by the SetSink call
// that returned id. If a later SetSink has already swapped to a newer
// sink, ClearSink is a no-op — that's how the new attach is protected
// from a stale per-stream goroutine's tear-down call.
func (s *Session) ClearSink(id uint64) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	if s.sinkID == id {
		s.sink = nil
	}
}

// SetRingAppend stores f as the function called for every PTYData
// payload right after the CWD tracker. Manager.Register installs this
// once when the entry is created; later calls overwrite. The function
// must be safe to call from the Pump goroutine.
func (s *Session) SetRingAppend(f RingFunc) {
	s.sinkMu.Lock()
	s.ringAppend = f
	s.sinkMu.Unlock()
}

// currentSinkAndRing returns the currently-installed sink and ring
// append in one critical section, so Pump dispatches to a consistent
// snapshot.
func (s *Session) currentSinkAndRing() (SinkFunc, RingFunc) {
	s.sinkMu.RLock()
	sink := s.sink
	ring := s.ringAppend
	s.sinkMu.RUnlock()
	return sink, ring
}

// Pump runs the session-lifetime output loop: reads bytes from the
// pseudo-console and dispatches them to the ring-append function (set
// once via SetRingAppend) and the currently-installed sink (set per
// attach via SetSink). On child exit it sends one final PTYFrame{Exit}
// to whichever sink is current at that moment.
//
// Pump must run in its own goroutine; the caller's request loop drives
// Write / Resize / Close on the same Session concurrently. ctx is the
// "kill the session" signal — it triggers s.Close so Read unblocks
// and Pump returns. Sink failures are explicitly NOT a kill signal:
// the session keeps running so a future reattach can rebind.
//
// ConPTY's Read does not unblock when the child exits — the pseudo-
// console pipe stays open until we close it. We use a small watcher
// goroutine that waits on the child and then closes the PTY, which
// causes Read to return the EOF that ends our loop. ctx cancellation
// goes through the same path.
func (s *Session) Pump(ctx context.Context) {
	type waitResult struct {
		code int
		err  error
	}
	waitCh := make(chan waitResult, 1)

	// Watcher 1: child process. Closing the PTY here unblocks the Read loop.
	go func() {
		code, err := s.p.Wait()
		_ = s.Close()
		waitCh <- waitResult{code: code, err: err}
	}()

	// Watcher 2: ctx cancellation. Translates into a Close so Read unblocks.
	stopCtxWatch := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Close()
		case <-stopCtxWatch:
		}
	}()
	defer close(stopCtxWatch)

	const chunkSize = 16 << 10 // 16 KiB
	buf := make([]byte, chunkSize)
	for {
		n, err := s.p.Read(buf)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buf[:n])
			// Feed the tracker BEFORE forwarding so a fresh CWD is
			// committed by the time the client tries to call ListFiles
			// after seeing the prompt.
			if paths := s.tracker.Feed(payload); len(paths) > 0 {
				s.cwdMu.Lock()
				s.cwd = paths[len(paths)-1]
				s.cwdMu.Unlock()
			}
			sink, ring := s.currentSinkAndRing()
			if ring != nil {
				ring(payload)
			}
			if sink != nil {
				frame := &v1.PTYFrame{Kind: &v1.PTYFrame_Data{Data: &v1.PTYData{Data: payload}}}
				// Sink errors are intentionally swallowed: the per-stream
				// caller's wire.Write may fail because the client's
				// connection went away, but the session itself stays
				// alive so a later reattach can pick up where this one
				// left off. The next SetSink swap will install a healthy
				// sink in place of the failing one.
				_ = sink(frame)
			}
		}
		if err != nil {
			break
		}
	}

	res := <-waitCh
	exit := &v1.PTYExit{ExitCode: int32(res.code)}
	if res.err != nil && !errors.Is(res.err, io.EOF) {
		exit.Error = res.err.Error()
	}
	if sink, _ := s.currentSinkAndRing(); sink != nil {
		_ = sink(&v1.PTYFrame{Kind: &v1.PTYFrame_Exit{Exit: exit}})
	}
}

var errClosed = errors.New("ptyhost: session closed")
