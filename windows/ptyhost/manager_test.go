//go:build windows

package ptyhost_test

import (
	"strings"
	"testing"
	"time"

	"github.com/peersh/peersh/windows/ptyhost"
)

const testOwner ptyhost.Owner = "OWNER1"

func TestRingBufferUnderCap(t *testing.T) {
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h, _ := m.Register(testOwner, s, "test")
	m.Append(testOwner, h, []byte("hello"))
	got, err := m.Attach(testOwner, h)
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if string(got.Replay) != "hello" {
		t.Fatalf("replay mismatch: %q", got.Replay)
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
	h, reg := m.Register(testOwner, s, "test")
	m.Detach(testOwner, h, reg.Gen)
	time.Sleep(150 * time.Millisecond)
	if n := m.Sweep(); n != 1 {
		t.Fatalf("expected 1 evicted, got %d", n)
	}
	if _, ok := m.Get(testOwner, h); ok {
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
	h, reg := m.Register(testOwner, s, "pwsh")
	listings := m.List(testOwner)
	if len(listings) != 1 || listings[0].Handle != h || !listings[0].Attached {
		t.Fatalf("expected one attached entry, got %v", listings)
	}
	m.Detach(testOwner, h, reg.Gen)
	listings = m.List(testOwner)
	if listings[0].Attached {
		t.Fatal("expected detached after Detach")
	}
}

// TestAttachAcrossReconnect models the bug this whole change exists to
// fix: the Manager survives a "QUIC drop" (Detach without Close), and a
// subsequent Attach with the same Owner reattaches without losing
// scrollback.
func TestAttachAcrossReconnect(t *testing.T) {
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h, reg := m.Register(testOwner, s, "pwsh")
	m.Append(testOwner, h, []byte("first run output"))
	m.Detach(testOwner, h, reg.Gen) // simulate the QUIC connection ending

	got, err := m.Attach(testOwner, h)
	if err != nil {
		t.Fatalf("reattach after detach: %v", err)
	}
	if got.Session != s {
		t.Fatal("reattach returned a different Session instance")
	}
	if string(got.Replay) != "first run output" {
		t.Fatalf("replay lost across reconnect: %q", got.Replay)
	}
	if got.Gen <= reg.Gen {
		t.Fatalf("attach gen should bump on reattach: reg=%d got=%d", reg.Gen, got.Gen)
	}
}

// TestAttachWithDifferentOwnerFails verifies the owner partition: a
// different session_id may not see, attach to, or otherwise touch
// another session's PTY. Returning the same "unknown handle" error as
// for a missing entry avoids leaking handle existence.
func TestAttachWithDifferentOwnerFails(t *testing.T) {
	const otherOwner ptyhost.Owner = "OWNER2"
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h, reg := m.Register(testOwner, s, "pwsh")
	m.Detach(testOwner, h, reg.Gen)

	if _, err := m.Attach(otherOwner, h); err == nil ||
		!strings.Contains(err.Error(), "unknown handle") {
		t.Fatalf("expected unknown-handle error for cross-owner Attach, got %v", err)
	}
	// The original owner must still be able to reattach: the failed
	// cross-owner Attach must not have flipped the entry's state.
	got, err := m.Attach(testOwner, h)
	if err != nil {
		t.Fatalf("legitimate owner reattach: %v", err)
	}
	if got.Session != s {
		t.Fatal("reattach returned a different Session instance")
	}
}

// TestAttachStealsFromExistingAttach is the reconnect-while-old-still-
// attached scenario: a same-owner Attach succeeds even when the entry
// is currently attached, and the previous attach's kick channel is
// closed so its goroutine can wind down. Pre-fix this returned
// alreadyAttached=true and rejected the reattach.
func TestAttachStealsFromExistingAttach(t *testing.T) {
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	_, reg := m.Register(testOwner, s, "pwsh")
	prevKick := reg.Kick

	got, err := m.Attach(testOwner, m.List(testOwner)[0].Handle)
	if err != nil {
		t.Fatalf("steal Attach: %v", err)
	}
	select {
	case <-prevKick:
	default:
		t.Fatal("previous attach's kick channel should be closed by steal")
	}
	if got.Gen == reg.Gen {
		t.Fatalf("attach gen should bump on steal: was=%d", got.Gen)
	}
	// New kick channel must NOT be the previous one.
	if got.Kick == prevKick {
		t.Fatal("Attach must hand back a fresh kick channel")
	}
}

// TestListIsOwnerScoped verifies that List is a per-owner view, so the
// file-API ListPTYs response cannot enumerate handles owned by a
// different session.
func TestListIsOwnerScoped(t *testing.T) {
	const otherOwner ptyhost.Owner = "OWNER2"
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s1, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s1.Close() })
	s2, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })

	h1, _ := m.Register(testOwner, s1, "pwsh")
	_, _ = m.Register(otherOwner, s2, "pwsh")

	listings := m.List(testOwner)
	if len(listings) != 1 || listings[0].Handle != h1 {
		t.Fatalf("List(testOwner) should return only h1; got %v", listings)
	}
	if got := m.List(otherOwner); len(got) != 1 {
		t.Fatalf("List(otherOwner) should return one entry; got %v", got)
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
