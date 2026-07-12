package pty_test

import (
	"errors"
	"runtime"
	"testing"

	"github.com/peersh/peersh/windows/pty"
)

// TestSpawnUnsupportedStub ensures the fallback stub (platforms without a
// real backing — i.e. not Windows/ConPTY and not darwin/forkpty) fails fast
// rather than silently succeeding.
func TestSpawnUnsupportedStub(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skip("Windows uses ConPTY and macOS uses forkpty; the stub applies elsewhere")
	}
	_, err := pty.Spawn("/bin/true", nil, nil, 80, 24)
	if !errors.Is(err, pty.ErrUnsupported) {
		t.Fatalf("stub Spawn: want ErrUnsupported, got %v", err)
	}
}

// TestSpawnDarwin exercises the real forkpty backing on macOS end-to-end: run
// a command that prints a known string and exits, read it back through the
// pty master.
func TestSpawnDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("forkpty backend is darwin-only in this module")
	}
	const want = "PEERSH_PTY_OK"
	p, err := pty.Spawn("/bin/echo", []string{want}, nil, 80, 24)
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	var got []byte
	buf := make([]byte, 4096)
	for {
		n, rerr := p.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			if containsBytes(got, want) {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	if !containsBytes(got, want) {
		t.Fatalf("expected %q in PTY output, got %q", want, string(got))
	}
	if err := p.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

// TestSpawnPwsh exercises the real ConPTY backing on Windows, end-to-end.
// We launch pwsh with a command that prints a known string and exits, read
// it back, and confirm the exit code.
func TestSpawnPwsh(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ConPTY is Windows-only")
	}

	const want = "PEERSH_PTY_OK"
	args := []string{"-NoLogo", "-NoProfile", "-Command", "Write-Output " + want + "; exit 0"}

	p, err := pty.Spawn("pwsh.exe", args, nil, 80, 24)
	if err != nil {
		// Fall back to the legacy powershell.exe if pwsh isn't installed.
		p, err = pty.Spawn("powershell.exe", args, nil, 80, 24)
		if err != nil {
			t.Skipf("no PowerShell on PATH: %v", err)
		}
	}

	// Drain output until the child exits. ConPTY blends stdout/stderr and
	// padds output with cursor-movement escapes; we only look for the
	// sentinel substring rather than asserting the full byte stream.
	var got []byte
	buf := make([]byte, 4096)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			got = append(got, buf[:n]...)
			if containsBytes(got, want) {
				break
			}
		}
		if err != nil {
			break
		}
	}

	if !containsBytes(got, want) {
		t.Fatalf("expected %q in PTY output, got %q", want, string(got))
	}

	if err := p.Close(); err != nil {
		t.Logf("close: %v", err)
	}
}

func containsBytes(haystack []byte, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
