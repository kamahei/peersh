// Package ptyhost wires a windows/pty.PTY to peersh's PTY protocol frames
// (peersh.v1.PTYInput / PTYResize / PTYData / PTYExit).
//
// Lifetime is per-stream: every PTYRequest opens a fresh ptyhost.Session;
// closing the wire stream tears the session and its child process down.
// PTY reattach across reconnects is intentionally out of Tier 1 scope —
// it would interleave badly with idle eviction without a corresponding
// scrollback ring buffer (deferred to Phase 6b).
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

// Session wraps a pty.PTY and exposes Write / Resize / Close. The pump
// goroutine inside Run drives the output direction.
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

// Pump runs the output loop: reads bytes from the pseudo-console and pushes
// them to the supplied sink as PTYFrame{Data} messages until the child
// exits, ctx is cancelled, or the sink returns an error. On exit it sends
// one terminating PTYFrame{Exit} carrying whatever the child returned.
//
// Pump must run in its own goroutine; the caller's request loop drives
// Write / Resize / Close on the same Session concurrently.
//
// ConPTY's Read does not unblock when the child exits — the pseudo-console
// pipe stays open until we close it. We use a small watcher goroutine that
// waits on the child and then closes the PTY, which causes Read to return
// the EOF that ends our loop. ctx cancellation goes through the same path.
func (s *Session) Pump(ctx context.Context, sink func(*v1.PTYFrame) error) {
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
			frame := &v1.PTYFrame{Kind: &v1.PTYFrame_Data{Data: &v1.PTYData{Data: payload}}}
			if serr := sink(frame); serr != nil {
				_ = s.Close()
				break
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
	_ = sink(&v1.PTYFrame{Kind: &v1.PTYFrame_Exit{Exit: exit}})
}

var errClosed = errors.New("ptyhost: session closed")
