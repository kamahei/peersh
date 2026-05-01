//go:build windows

package ptyhost_test

import (
	"strings"
	"testing"
	"time"

	"github.com/peersh/peersh/windows/ptyhost"
)

func TestRingBufferUnderCap(t *testing.T) {
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := m.Register(s, "test")
	m.Append(h, []byte("hello"))
	_, replay, _, err := m.Attach(h)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if string(replay) != "hello" {
		t.Fatalf("replay mismatch: %q", replay)
	}
}

func TestSweepDropsExpired(t *testing.T) {
	m := ptyhost.NewManager()
	m.SetIdleTimeout(50 * time.Millisecond)
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	h := m.Register(s, "test")
	m.Detach(h)
	time.Sleep(150 * time.Millisecond)
	if n := m.Sweep(); n != 1 {
		t.Fatalf("expected 1 evicted, got %d", n)
	}
	if _, ok := m.Get(h); ok {
		t.Fatal("entry should be gone after sweep")
	}
}

func TestListReportsAttachState(t *testing.T) {
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := m.Register(s, "pwsh")
	listings := m.List()
	if len(listings) != 1 || listings[0].Handle != h || !listings[0].Attached {
		t.Fatalf("expected one attached entry, got %v", listings)
	}
	m.Detach(h)
	listings = m.List()
	if listings[0].Attached {
		t.Fatal("expected detached after Detach")
	}
}

// mustResolveShellForTest finds a Windows shell to spawn under a PTY
// for these tests. Skips the test on hosts with no PowerShell or cmd.
func mustResolveShellForTest(t *testing.T) (string, []string) {
	t.Helper()
	for _, exe := range []string{"pwsh.exe", "powershell.exe"} {
		if path, err := lookExe(exe); err == nil {
			return path, []string{"-NoLogo", "-NoProfile", "-Command", "Start-Sleep -Seconds 30"}
		}
	}
	t.Skip("no PowerShell on PATH")
	return "", nil
}

func lookExe(name string) (string, error) {
	// Avoid os/exec.LookPath here so the test compiles cleanly without
	// importing os/exec at the top of the file.
	if !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	// Defer to the standard library indirectly.
	return execLookPath(name)
}
