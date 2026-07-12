// Package pty exposes a minimal pseudo-console interface used by peershd to
// run interactive programs (PowerShell, claude, codex, ...) against a
// ConPTY-backed child process.
//
// The interface is OS-agnostic so that callers and tests compile on any
// platform; the real implementation (pty_windows.go) is only built on
// Windows. Other operating systems get the stub in pty_other.go that fails
// fast with ErrUnsupported — peershd is Windows-only by design.
package pty

import (
	"errors"
	"io"
)

// ErrUnsupported is returned when the host OS lacks the ConPTY API
// (Windows 10 1809 / Server 2019 or later are required).
var ErrUnsupported = errors.New("pty: pseudo-console requires Windows 10 1809 or later")

// PTY is a live pseudo-console attached to a child process.
//
// Read pulls bytes the child wrote to its stdout/stderr (already merged by
// ConPTY). Write delivers user keystrokes / paste payloads to the child's
// stdin. Resize tells the child the terminal grid dimensions changed, so
// programs that re-flow output (PowerShell, less, claude) can react.
//
// Close terminates the child if it is still running and releases all
// pseudo-console handles. Wait blocks until the child exits and returns
// the exit code.
type PTY interface {
	io.ReadWriteCloser
	Resize(cols, rows uint16) error
	Wait() (exitCode int, err error)
}

// Spawn creates a pseudo-console of the given size, starts the executable
// with args, and returns a PTY bound to that process.
//
// env is a list of "KEY=VALUE" entries appended to the child's environment
// (on top of the inherited process environment) — used e.g. to point a zsh at
// a ZDOTDIR that installs the OSC 9;9 cwd-tracking hook. Ignored on Windows,
// where shells inherit the process environment and the hook is delivered via
// the command line instead. nil for a plain (unwrapped) command.
//
// The caller MUST Close the PTY when finished, which terminates the child
// process and releases the pseudo-console handles.
func Spawn(executable string, args, env []string, cols, rows uint16) (PTY, error) {
	return spawn(executable, args, env, cols, rows)
}
