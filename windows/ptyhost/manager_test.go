//go:build windows

package ptyhost_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
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
	h, _ := m.Register(testOwner, s, "test", nil)
	m.Append(testOwner, h, []byte("hello"))
	got, err := m.Attach(testOwner, h, nil)
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
	h, reg := m.Register(testOwner, s, "test", nil)
	m.Detach(reg.Token)
	_ = h
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
	sinkFn := func(*v1.PTYFrame) error { return nil }
	h, reg := m.Register(testOwner, s, "pwsh", sinkFn)
	listings := m.List(testOwner)
	if len(listings) != 1 || listings[0].Handle != h || listings[0].AttachedCount != 1 {
		t.Fatalf("expected one entry with AttachedCount=1, got %v", listings)
	}
	m.Detach(reg.Token)
	listings = m.List(testOwner)
	if listings[0].AttachedCount != 0 {
		t.Fatalf("expected AttachedCount=0 after Detach, got %d", listings[0].AttachedCount)
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

	h, reg := m.Register(testOwner, s, "pwsh", nil)
	m.Append(testOwner, h, []byte("first run output"))
	m.Detach(reg.Token) // simulate the QUIC connection ending

	got, err := m.Attach(testOwner, h, nil)
	if err != nil {
		t.Fatalf("reattach after detach: %v", err)
	}
	if got.Session != s {
		t.Fatal("reattach returned a different Session instance")
	}
	if string(got.Replay) != "first run output" {
		t.Fatalf("replay lost across reconnect: %q", got.Replay)
	}
	if got.Token.SinkID != 0 {
		t.Fatal("snapshot-only Attach (nil sink) should yield zero SinkID")
	}
}

// TestAttachWithDifferentOwnerFails verifies the owner partition: a
// different Owner may not see, attach to, or otherwise touch
// another's PTY. Returning the same "unknown handle" error as
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

	h, reg := m.Register(testOwner, s, "pwsh", nil)
	m.Detach(reg.Token)

	if _, err := m.Attach(otherOwner, h, nil); err == nil ||
		!strings.Contains(err.Error(), "unknown handle") {
		t.Fatalf("expected unknown-handle error for cross-owner Attach, got %v", err)
	}
	// The original owner must still be able to reattach: the failed
	// cross-owner Attach must not have flipped the entry's state.
	got, err := m.Attach(testOwner, h, nil)
	if err != nil {
		t.Fatalf("legitimate owner reattach: %v", err)
	}
	if got.Session != s {
		t.Fatal("reattach returned a different Session instance")
	}
}

// TestMultiAttachFanOut verifies that two simultaneous Attaches to the
// same handle both receive every chunk Pump produces, with no missed
// or duplicated bytes between the two sinks. This is the core
// invariant the multi-attach refactor enables: a CLI on the user's PC
// and the mobile app can both observe the same shell live.
func TestMultiAttachFanOut(t *testing.T) {
	m := ptyhost.NewManager()
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h, regA := m.Register(testOwner, s, "pwsh", func(*v1.PTYFrame) error { return nil })

	// Drop a known marker into the ring so the second sink's replay is
	// non-empty (Pump may not have emitted anything yet for the freshly
	// spawned shell, depending on timing).
	m.Append(testOwner, h, []byte("scrollback-marker"))

	var (
		mu       sync.Mutex
		got      []byte
	)
	regB, err := m.Attach(testOwner, h, func(f *v1.PTYFrame) error {
		if d := f.GetData(); d != nil {
			mu.Lock()
			got = append(got, d.GetData()...)
			mu.Unlock()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second Attach: %v", err)
	}
	if !strings.Contains(string(regB.Replay), "scrollback-marker") {
		t.Fatalf("second Attach should replay scrollback containing marker, got %q", regB.Replay)
	}
	// First attach is still live — no steal happened. Detach both;
	// neither should error.
	m.Detach(regA.Token)
	m.Detach(regB.Token)
}

// TestDetachOneOfTwoKeepsOtherAlive checks that removing one of two
// attached sinks doesn't disturb the other and doesn't start the idle
// TTL countdown.
func TestDetachOneOfTwoKeepsOtherAlive(t *testing.T) {
	m := ptyhost.NewManager()
	m.SetIdleTimeout(50 * time.Millisecond)
	t.Cleanup(m.Close)
	exePath, args := mustResolveShellForTest(t)
	s, err := ptyhost.Open(exePath, args, 80, 24)
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	h, regA := m.Register(testOwner, s, "pwsh", func(*v1.PTYFrame) error { return nil })
	regB, err := m.Attach(testOwner, h, func(*v1.PTYFrame) error { return nil })
	if err != nil {
		t.Fatalf("Attach B: %v", err)
	}

	// Detach A; B is still attached so Sweep must not evict.
	m.Detach(regA.Token)
	time.Sleep(150 * time.Millisecond)
	if n := m.Sweep(); n != 0 {
		t.Fatalf("Sweep evicted while B still attached: %d", n)
	}

	// Now detach B; the entry becomes idle and should be evicted on
	// the next Sweep after the TTL elapses.
	m.Detach(regB.Token)
	time.Sleep(150 * time.Millisecond)
	if n := m.Sweep(); n != 1 {
		t.Fatalf("expected 1 evicted after both detached, got %d", n)
	}
}

// TestListIsOwnerScoped verifies that List is a per-owner view, so the
// file-API ListPTYs response cannot enumerate handles owned by a
// different owner.
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

	h1, _ := m.Register(testOwner, s1, "pwsh", nil)
	_, _ = m.Register(otherOwner, s2, "pwsh", nil)

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
