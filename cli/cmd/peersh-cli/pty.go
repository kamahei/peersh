// peersh-cli's PTY mode. Wires the local terminal's stdin/stdout to a
// remote pseudo-console exposed by peershd via the v1.PTYRequest envelope.
//
// Tier 1 of Phase 8: minimum viable interactive client. Raw mode is
// enabled on stdin so keystrokes flow through verbatim (arrow keys,
// Ctrl+C, Tab completion); resize tracking is best-effort via x/term's
// GetSize, polled at 1 Hz because Windows console doesn't deliver
// SIGWINCH and golang.org/x/term doesn't expose a cross-platform watch
// API. The mobile app is the canonical user-facing client; this CLI
// path mainly exists for fast end-to-end protocol verification on the
// host machine.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/core/transport"
	"github.com/peersh/peersh/core/wire"

	"golang.org/x/term"
)

// runPTY opens a PTY stream. When reattachHandle is non-empty the
// server rebinds an existing persisted shell and replays its
// scrollback (multi-attach: the server does not displace any other
// streams already attached to the same handle). When empty a fresh
// PTY is spawned with the given command.
func runPTY(ctx context.Context, conn *transport.Conn, command, reattachHandle string) error {
	stream, err := conn.OpenStream(ctx)
	if err != nil {
		return fmt.Errorf("OpenStream: %w", err)
	}
	defer stream.Close()

	cols, rows := initialSize()
	if err := wire.Write(stream, &v1.StreamRequest{
		Kind: &v1.StreamRequest_Pty{Pty: &v1.PTYRequest{
			Command:        command,
			Cols:           uint32(cols),
			Rows:           uint32(rows),
			ReattachHandle: reattachHandle,
		}},
	}); err != nil {
		return fmt.Errorf("write StreamRequest: %w", err)
	}

	// Put stdin in raw mode if it's a tty, so keystrokes flow through
	// verbatim. Restored on exit.
	stdinFD := int(os.Stdin.Fd())
	var oldState *term.State
	if term.IsTerminal(stdinFD) {
		s, err := term.MakeRaw(stdinFD)
		if err == nil {
			oldState = s
			defer term.Restore(stdinFD, oldState)
		}
	}

	var writeMu sync.Mutex
	send := func(f *v1.PTYFrame) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		return wire.Write(stream, f)
	}

	// Resize watcher: poll x/term.GetSize at 1 Hz while the session lives
	// and emit PTYResize on changes.
	resizeStop := make(chan struct{})
	defer close(resizeStop)
	go watchResize(stdinFD, cols, rows, send, resizeStop)

	// Stdin -> PTYInput.
	go func() {
		buf := make([]byte, 4096)
		stdin := os.Stdin
		for {
			n, err := stdin.Read(buf)
			if n > 0 {
				payload := make([]byte, n)
				copy(payload, buf[:n])
				if serr := send(&v1.PTYFrame{Kind: &v1.PTYFrame_Input{Input: &v1.PTYInput{Data: payload}}}); serr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// PTYData -> stdout.
	r := wire.NewReader(stream)
	w := bufio.NewWriter(os.Stdout)
	defer w.Flush()
	for {
		frame := &v1.PTYFrame{}
		if err := wire.Read(r, frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("read PTYFrame: %w", err)
		}
		switch k := frame.GetKind().(type) {
		case *v1.PTYFrame_ReattachAck:
			if !k.ReattachAck.GetAccepted() {
				fmt.Fprintf(os.Stderr, "reattach rejected: %s\r\n", k.ReattachAck.GetReason())
				return fmt.Errorf("reattach rejected: %s", k.ReattachAck.GetReason())
			}
			// Print the server-issued handle to stderr so scripts can
			// capture it (e.g. peersh-cli ... -pty-new 2>handle.txt).
			fmt.Fprintf(os.Stderr, "pty handle: %s\r\n", k.ReattachAck.GetHandle())
		case *v1.PTYFrame_Data:
			if d := k.Data.GetData(); len(d) > 0 {
				_, _ = w.Write(d)
				_ = w.Flush()
			}
		case *v1.PTYFrame_Exit:
			fmt.Fprintf(os.Stderr, "\r\npty exited: code=%d err=%q\r\n", k.Exit.GetExitCode(), k.Exit.GetError())
			return nil
		}
	}
}

func initialSize() (cols, rows int) {
	cols, rows, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || cols <= 0 || rows <= 0 {
		return 80, 24
	}
	return cols, rows
}

func watchResize(fd, lastCols, lastRows int, send func(*v1.PTYFrame) error, stop <-chan struct{}) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c, r, err := term.GetSize(fd)
			if err != nil || c <= 0 || r <= 0 {
				continue
			}
			if c == lastCols && r == lastRows {
				continue
			}
			lastCols, lastRows = c, r
			_ = send(&v1.PTYFrame{Kind: &v1.PTYFrame_Resize{Resize: &v1.PTYResize{Cols: uint32(c), Rows: uint32(r)}}})
		}
	}
}
