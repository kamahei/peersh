// Package ptyhost wires a windows/pty.PTY to peersh's PTY protocol frames
// (peersh.v1.PTYInput / PTYResize / PTYData / PTYExit).
//
// Sessions persist across QUIC reconnects: a Session opened during one
// connection is held by the process-global ptyhost.Manager (see
// manager.go) and can be rebound by a later connection that presents
// the matching reattach handle plus the same Owner. Manager.Sweep
// reaps detached Sessions whose IdleTimeout has elapsed.
//
// Pump is session-lifetime: Manager.Register starts one Pump goroutine
// per Session and the same goroutine runs until the child process
// exits (or the Manager evicts the entry). Output bytes flow into
// (a) the entry's ring buffer (always) and (b) every currently-
// attached sink (zero or more — multiple clients can attach to the
// same Session simultaneously and all receive the same byte stream).
// Sinks come and go via AddSink / RemoveSink as clients attach and
// detach; the Session itself stays alive for the Manager's idle TTL.
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
// Pump. Returning an error is harmless — Pump just drops the frame for
// that sink and keeps running, so a single broken stream does not
// terminate the Session or starve the other attached sinks.
type SinkFunc func(*v1.PTYFrame) error

// RingFunc is called for every PTYData payload before sink dispatch so
// the caller (Manager) can mirror the bytes into the per-entry
// scrollback ring buffer.
type RingFunc func([]byte)

// SinkToken identifies an installed sink. AddSink returns one;
// RemoveSink takes one back. Tokens are unique within a Session and
// monotonically increase, so a stale RemoveSink call cannot remove a
// later sink that happens to reuse the underlying slot.
type SinkToken uint64

// Session wraps a pty.PTY and exposes Write / Resize / Close. The
// session-lifetime Pump goroutine inside Manager drives the output
// direction; per-stream sinks are added via AddSink and removed via
// RemoveSink. Any number of sinks can be attached at once and Pump
// dispatches each chunk to all of them (multi-attach fan-out), so
// e.g. a CLI on the operator's PC and the mobile app can observe the
// same shell live.
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

	// sinkMu serialises Pump's per-chunk dispatch with sink mutation
	// (AddSink/RemoveSink) and with ring-snapshot-during-attach. Pump
	// holds it across (ring write + sink iteration) so a concurrent
	// Attach cannot observe a state where a chunk has been written to
	// the ring but the new sink missed the live dispatch (or vice
	// versa).
	sinkMu     sync.Mutex
	sinks      map[SinkToken]SinkFunc
	nextSink   SinkToken
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
// Calls are serialised so concurrent input from multiple attached
// clients lands as whole chunks rather than interleaving mid-byte.
func (s *Session) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errClosed
	}
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

// AddSink registers sink to receive every Pump frame and returns a
// token the caller passes to RemoveSink when it's done. Multiple sinks
// can be attached simultaneously; Pump dispatches each chunk to all of
// them in arbitrary order.
func (s *Session) AddSink(sink SinkFunc) SinkToken {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	return s.addSinkLocked(sink)
}

// RemoveSink uninstalls the sink identified by token. A no-op when the
// token has already been removed (or never existed).
func (s *Session) RemoveSink(token SinkToken) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	delete(s.sinks, token)
}

// AddSinkWithSnapshot atomically captures a caller-supplied snapshot
// (typically a ring-buffer scrollback) AND installs sink, so the new
// sink doesn't miss any chunk that occurred between snapshot and
// registration. snapshotFn is invoked while the sink lock is held —
// avoid expensive work or blocking calls inside it.
func (s *Session) AddSinkWithSnapshot(snapshotFn func() []byte, sink SinkFunc) (SinkToken, []byte) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	snap := snapshotFn()
	token := s.addSinkLocked(sink)
	return token, snap
}

func (s *Session) addSinkLocked(sink SinkFunc) SinkToken {
	s.nextSink++
	if s.sinks == nil {
		s.sinks = make(map[SinkToken]SinkFunc)
	}
	s.sinks[s.nextSink] = sink
	return s.nextSink
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

// Pump runs the session-lifetime output loop: reads bytes from the
// pseudo-console, mirrors them into the ring-append function (set
// once via SetRingAppend), and dispatches them to every currently-
// attached sink. On child exit it sends one final PTYFrame{Exit} to
// every sink that's still attached at that moment.
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
			// Feed the tracker BEFORE dispatch so a fresh CWD is
			// committed by the time the client tries to call ListFiles
			// after seeing the prompt.
			if paths := s.tracker.Feed(payload); len(paths) > 0 {
				s.cwdMu.Lock()
				s.cwd = paths[len(paths)-1]
				s.cwdMu.Unlock()
			}
			s.dispatchData(payload)
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
	s.dispatchExit(exit)
}

// dispatchData mirrors payload into the ring-append callback and
// fans out a Data frame to every attached sink. Holding sinkMu across
// both operations is what makes Manager.Attach's snapshot-and-add path
// race-free: a concurrent attach either sees this chunk in the ring
// snapshot OR receives it via the new sink, never both and never
// neither.
func (s *Session) dispatchData(payload []byte) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	if s.ringAppend != nil {
		s.ringAppend(payload)
	}
	if len(s.sinks) == 0 {
		return
	}
	frame := &v1.PTYFrame{Kind: &v1.PTYFrame_Data{Data: &v1.PTYData{Data: payload}}}
	for _, fn := range s.sinks {
		// Sink errors are intentionally swallowed: the per-stream
		// caller's wire.Write may fail because the client's
		// connection went away, but the session itself stays alive
		// so a later reattach can pick up where this one left off.
		// The next AddSink will install a healthy sink in place of
		// the failing one.
		_ = fn(frame)
	}
}

func (s *Session) dispatchExit(exit *v1.PTYExit) {
	s.sinkMu.Lock()
	defer s.sinkMu.Unlock()
	if len(s.sinks) == 0 {
		return
	}
	frame := &v1.PTYFrame{Kind: &v1.PTYFrame_Exit{Exit: exit}}
	for _, fn := range s.sinks {
		_ = fn(frame)
	}
}

var errClosed = errors.New("ptyhost: session closed")
