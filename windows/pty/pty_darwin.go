//go:build darwin

package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	cpty "github.com/creack/pty"
)

// unixPTY is the darwin (forkpty) backing for the PTY interface, built on
// github.com/creack/pty. It mirrors the ConPTY behaviour in pty_windows.go:
// Read/Write proxy the pty master, Resize issues TIOCSWINSZ, Wait blocks on
// the child and reports its exit code (with a nil error even for a non-zero
// or signalled exit, matching the contract ptyhost.Pump relies on), and Close
// terminates the child and releases the master.
type unixPTY struct {
	master *os.File
	cmd    *exec.Cmd
}

func spawn(executable string, args, env []string, cols, rows uint16) (PTY, error) {
	cmd := exec.Command(executable, args...)
	// A pseudo-terminal without TERM makes line-editing shells and full-
	// screen programs misbehave; peershd may be launched by launchd with a
	// minimal environment, so set a sane default while preserving the rest.
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	// Caller-supplied extras (e.g. ZDOTDIR for the zsh OSC 9;9 cwd hook) win
	// over the inherited values since they are appended last.
	cmd.Env = append(cmd.Env, env...)
	master, err := cpty.StartWithSize(cmd, &cpty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", executable, err)
	}
	return &unixPTY{master: master, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.master.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.master.Write(b) }

func (p *unixPTY) Resize(cols, rows uint16) error {
	if err := cpty.Setsize(p.master, &cpty.Winsize{Cols: cols, Rows: rows}); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}

func (p *unixPTY) Wait() (int, error) {
	err := p.cmd.Wait()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// Normal child termination (possibly non-zero, or killed by a
			// signal → ExitCode() == -1). Report the code with a nil error so
			// Pump treats it as a clean session end, not a host fault.
			return ee.ExitCode(), nil
		}
		return -1, fmt.Errorf("pty: wait: %w", err)
	}
	return 0, nil
}

func (p *unixPTY) Close() error {
	// Terminate the child if it is still running, then release the master.
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.master.Close()
}
