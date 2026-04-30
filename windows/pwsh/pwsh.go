// Package pwsh wraps a long-lived PowerShell process and runs commands
// against it while preserving session state (cwd, variables) across
// invocations.
//
// Phase 1 strategy: spawn pwsh.exe (or powershell.exe as fallback) with
// `-NoExit -Command -`, feeding commands through stdin, detecting end of
// command by appending a per-command sentinel marker that the host watches
// for in the stdout stream.
//
// Limitations carried into Phase 1:
//   - One Exec at a time per Host. Callers serialize Exec calls; a second
//     Exec while another is still being drained returns ErrBusy.
//   - Stderr emitted in the last few microseconds before the sentinel may
//     occasionally appear in the next Exec's Output. Phase 6's
//     SessionManager rework will revisit completion detection.
package pwsh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Stream tags a chunk of output by its source pipe.
type Stream int

const (
	Stdout Stream = iota
	Stderr
)

// String makes Stream printable for logs.
func (s Stream) String() string {
	if s == Stderr {
		return "stderr"
	}
	return "stdout"
}

// Chunk is one delivered piece of command output. Data ends in '\n' when the
// underlying read produced a complete line.
type Chunk struct {
	Stream Stream
	Data   []byte
}

// ErrBusy is returned by Exec when another Exec on the same Host has not yet
// reported io.EOF.
var ErrBusy = errors.New("pwsh: another Exec is still active on this Host")

// ErrClosed is returned after Host.Close has been called.
var ErrClosed = errors.New("pwsh: host is closed")

// Host wraps a single long-lived PowerShell process.
type Host struct {
	cmd      *exec.Cmd
	path     string
	stdinW   io.WriteCloser
	stdoutR  io.ReadCloser
	stderrR  io.ReadCloser
	stdoutCh chan []byte
	stderrCh chan []byte

	execMu  sync.Mutex // taken for the lifetime of an Exec
	stateMu sync.Mutex
	active  bool // an Exec is in flight
	closed  bool

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// Start launches a PowerShell host process. It first looks for `pwsh.exe`
// (PowerShell 7) on PATH; if absent, it falls back to `powershell.exe`.
func Start(ctx context.Context) (*Host, error) {
	path, err := exec.LookPath("pwsh.exe")
	if err != nil {
		path, err = exec.LookPath("powershell.exe")
		if err != nil {
			return nil, fmt.Errorf("pwsh: no pwsh.exe or powershell.exe on PATH: %w", err)
		}
	}

	cmd := exec.CommandContext(ctx, path, "-NoLogo", "-NoProfile", "-NoExit", "-Command", "-")

	stdinW, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("pwsh: stdin: %w", err)
	}
	stdoutR, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pwsh: stdout: %w", err)
	}
	stderrR, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("pwsh: stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pwsh: start: %w", err)
	}

	h := &Host{
		cmd:      cmd,
		path:     path,
		stdinW:   stdinW,
		stdoutR:  stdoutR,
		stderrR:  stderrR,
		stdoutCh: make(chan []byte, 256),
		stderrCh: make(chan []byte, 256),
	}
	h.wg.Add(2)
	go h.pumpLines(stdoutR, h.stdoutCh)
	go h.pumpLines(stderrR, h.stderrCh)
	return h, nil
}

// Path returns the executable path the Host was launched with. Useful for
// logs that need to be honest about which PowerShell variant is in use.
func (h *Host) Path() string { return h.path }

// pumpLines reads complete lines from r and forwards them on out. Trailing
// newlines are preserved on each delivered slice. The goroutine exits when r
// returns io.EOF or any other error.
func (h *Host) pumpLines(r io.Reader, out chan<- []byte) {
	defer h.wg.Done()
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			out <- line
		}
		if err != nil {
			return
		}
	}
}

// Exec sends cmd to the Host and returns an Output stream that yields the
// command's stdout/stderr until completion.
//
// Only one Exec may be active per Host at a time.
func (h *Host) Exec(_ context.Context, cmd string) (*Output, error) {
	h.execMu.Lock()
	h.stateMu.Lock()
	if h.closed {
		h.stateMu.Unlock()
		h.execMu.Unlock()
		return nil, ErrClosed
	}
	if h.active {
		h.stateMu.Unlock()
		h.execMu.Unlock()
		return nil, ErrBusy
	}
	h.active = true
	h.stateMu.Unlock()

	tokenBytes := make([]byte, 8)
	if _, err := rand.Read(tokenBytes); err != nil {
		h.markIdle()
		h.execMu.Unlock()
		return nil, fmt.Errorf("pwsh: token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	sentinel := "__PEERSH_END_" + token + "__"

	// Two newline writes — one to terminate the user's command, one before
	// the sentinel — so the sentinel always lands on a fresh line.
	payload := cmd + "\r\nWrite-Output \"" + sentinel + "\"\r\n"
	if _, err := io.WriteString(h.stdinW, payload); err != nil {
		h.markIdle()
		h.execMu.Unlock()
		return nil, fmt.Errorf("pwsh: write stdin: %w", err)
	}

	return &Output{
		host:     h,
		sentinel: []byte(sentinel),
	}, nil
}

func (h *Host) markIdle() {
	h.stateMu.Lock()
	h.active = false
	h.stateMu.Unlock()
}

// Output is one Exec's stream of chunks.
type Output struct {
	host     *Host
	sentinel []byte
	closed   bool
	released bool
}

// Recv returns the next chunk of output, or io.EOF when the command has
// completed. After io.EOF, the next Exec on the parent Host can proceed.
func (o *Output) Recv(ctx context.Context) (Chunk, error) {
	if o.closed {
		o.release()
		return Chunk{}, io.EOF
	}
	select {
	case <-ctx.Done():
		return Chunk{}, ctx.Err()
	case line := <-o.host.stdoutCh:
		trimmed := bytes.TrimRight(line, "\r\n")
		if bytes.Equal(trimmed, o.sentinel) {
			o.closed = true
			o.release()
			return Chunk{}, io.EOF
		}
		return Chunk{Stream: Stdout, Data: copyBytes(line)}, nil
	case line := <-o.host.stderrCh:
		return Chunk{Stream: Stderr, Data: copyBytes(line)}, nil
	}
}

// release lets the parent Host accept its next Exec. Safe to call multiple
// times.
func (o *Output) release() {
	if o.released {
		return
	}
	o.released = true
	o.host.execMu.Unlock()
	o.host.markIdle()
}

func copyBytes(b []byte) []byte {
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

// Close shuts down the PowerShell process. It first writes `exit` to stdin,
// then waits with a short deadline before forcefully killing the process.
func (h *Host) Close() error {
	h.closeOnce.Do(func() {
		h.stateMu.Lock()
		h.closed = true
		h.stateMu.Unlock()

		// Best-effort polite shutdown.
		_, _ = io.WriteString(h.stdinW, "exit\r\n")
		_ = h.stdinW.Close()

		done := make(chan error, 1)
		go func() { done <- h.cmd.Wait() }()
		select {
		case err := <-done:
			h.closeErr = err
		case <-time.After(3 * time.Second):
			_ = h.cmd.Process.Kill()
			h.closeErr = errors.New("pwsh: timed out, killed")
			<-done
		}
		// pump goroutines exit when stdout/stderr close after process exit.
		h.wg.Wait()
	})
	return h.closeErr
}
