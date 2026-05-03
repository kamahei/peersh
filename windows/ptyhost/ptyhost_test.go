//go:build windows

package ptyhost_test

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	v1 "github.com/peersh/peersh/core/protocol/peersh/v1"
	"github.com/peersh/peersh/windows/ptyhost"
)

func mustHavePwsh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh.exe"); err == nil {
		return
	}
	if _, err := exec.LookPath("powershell.exe"); err == nil {
		return
	}
	t.Skip("no PowerShell on PATH")
}

func TestOpenAndPumpExits(t *testing.T) {
	mustHavePwsh(t)

	// Bypass the OSC 9;9 wrapper (shell.Resolve adds -NoExit) by passing
	// an absolute path verbatim. We just want a child that exits quickly
	// to confirm Pump emits an Exit terminator.
	exePath, err := exec.LookPath("pwsh.exe")
	if err != nil {
		exePath, err = exec.LookPath("powershell.exe")
		if err != nil {
			t.Skipf("no PowerShell on PATH: %v", err)
		}
	}
	s, err := ptyhost.Open(exePath, []string{"-NoLogo", "-NoProfile", "-Command", "exit 0"}, 80, 24)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		gotExit  bool
		gotData  bool
		dataSeen int
	)
	done := make(chan struct{})
	s.SetSink(func(f *v1.PTYFrame) error {
		mu.Lock()
		defer mu.Unlock()
		switch f.GetKind().(type) {
		case *v1.PTYFrame_Data:
			gotData = true
			dataSeen += len(f.GetData().GetData())
		case *v1.PTYFrame_Exit:
			gotExit = true
			close(done)
		}
		return nil
	})
	go func() {
		s.Pump(ctx)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("Pump did not exit in time; gotData=%v dataSeen=%d", gotData, dataSeen)
	}
	mu.Lock()
	defer mu.Unlock()
	if !gotExit {
		t.Fatal("expected an Exit frame")
	}
}
