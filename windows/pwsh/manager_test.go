//go:build windows

package pwsh_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/peersh/peersh/windows/pwsh"
)

func mustHavePwsh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh.exe"); err == nil {
		return
	}
	if _, err := exec.LookPath("powershell.exe"); err == nil {
		return
	}
	t.Skip("no pwsh.exe or powershell.exe on PATH")
}

func TestAttachOrCreateFreshThenReattach(t *testing.T) {
	mustHavePwsh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := pwsh.NewSessionManager()
	t.Cleanup(func() { _ = mgr.Close() })

	id1, h1, reattached, err := mgr.AttachOrCreate(ctx, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if reattached {
		t.Fatal("fresh session should not be reattached")
	}
	if id1 == "" {
		t.Fatal("expected non-empty session id")
	}

	mgr.Detach(id1)

	id2, h2, reattached, err := mgr.AttachOrCreate(ctx, id1)
	if err != nil {
		t.Fatalf("reattach: %v", err)
	}
	if !reattached {
		t.Fatal("expected reattach")
	}
	if id2 != id1 {
		t.Fatalf("session id changed: %s vs %s", id1, id2)
	}
	if h1 != h2 {
		t.Fatal("expected same Host on reattach")
	}
}

func TestAttachOrCreateUnknownSessionGetsFresh(t *testing.T) {
	mustHavePwsh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := pwsh.NewSessionManager()
	t.Cleanup(func() { _ = mgr.Close() })

	id, _, reattached, err := mgr.AttachOrCreate(ctx, "BOGUS00000000000")
	if err != nil {
		t.Fatalf("AttachOrCreate: %v", err)
	}
	if reattached {
		t.Fatal("unknown session id should produce a fresh session")
	}
	if id == "BOGUS00000000000" {
		t.Fatal("server should not honour bogus client-supplied session id")
	}
}

func TestSweepEvictsExpired(t *testing.T) {
	mustHavePwsh(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	mgr := pwsh.NewSessionManager()
	mgr.SetIdleTimeout(50 * time.Millisecond)
	t.Cleanup(func() { _ = mgr.Close() })

	id, _, _, err := mgr.AttachOrCreate(ctx, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr.Detach(id)

	time.Sleep(150 * time.Millisecond)
	evicted := mgr.Sweep()
	if evicted != 1 {
		t.Fatalf("expected 1 evicted, got %d", evicted)
	}

	// Subsequent AttachOrCreate with the same id should be a fresh
	// session (the old one was killed).
	id2, _, reattached, err := mgr.AttachOrCreate(ctx, id)
	if err != nil {
		t.Fatalf("create after eviction: %v", err)
	}
	if reattached {
		t.Fatal("expected a fresh session after sweep eviction")
	}
	if id2 == id {
		t.Fatalf("expected new session id, got %s == %s", id2, id)
	}
}
