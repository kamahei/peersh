//go:build windows

package pty

import (
	"context"
	"fmt"
	"strings"

	wcp "github.com/UserExistsError/conpty"
)

// winPTY is the Windows ConPTY backing for the PTY interface.
//
// We delegate to UserExistsError/conpty, which wraps the native
// CreatePseudoConsole / ResizePseudoConsole / STARTUPINFOEX dance behind a
// small ReadWriteCloser-shaped API. The wrapper is MIT-licensed and we
// keep its surface area narrow so swapping it out later (e.g. moving the
// syscalls in-tree) is a localized change.
type winPTY struct {
	cp *wcp.ConPty
}

func spawn(executable string, args, env []string, cols, rows uint16) (PTY, error) {
	// env is unused on Windows: PowerShell/cmd inherit the process environment
	// and receive the OSC 9;9 wrapper via -EncodedCommand / PROMPT, not env.
	_ = env
	cmdline := buildCmdline(executable, args)
	cp, err := wcp.Start(cmdline, wcp.ConPtyDimensions(int(cols), int(rows)))
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", executable, err)
	}
	return &winPTY{cp: cp}, nil
}

func (p *winPTY) Read(b []byte) (int, error)  { return p.cp.Read(b) }
func (p *winPTY) Write(b []byte) (int, error) { return p.cp.Write(b) }

func (p *winPTY) Resize(cols, rows uint16) error {
	if err := p.cp.Resize(int(cols), int(rows)); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}

func (p *winPTY) Wait() (int, error) {
	code, err := p.cp.Wait(context.Background())
	if err != nil {
		return -1, fmt.Errorf("pty: wait: %w", err)
	}
	return int(code), nil
}

func (p *winPTY) Close() error { return p.cp.Close() }

// buildCmdline composes a Windows command line out of an executable path and
// argument list. The conpty wrapper expects a single CreateProcess-style
// string, not an argv slice. A full Win32 escaper is overkill here; peershd
// only spawns shells with controlled argument vectors (see windows/shell).
func buildCmdline(executable string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteArg(executable))
	for _, a := range args {
		parts = append(parts, quoteArg(a))
	}
	return strings.Join(parts, " ")
}

func quoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if !strings.ContainsAny(s, " \t\"") {
		return s
	}
	// Conservative quoting: wrap the whole arg in double quotes and escape
	// embedded ones. PowerShell's -EncodedCommand payload is base64 with no
	// quotes or whitespace so it never reaches this branch in practice.
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
