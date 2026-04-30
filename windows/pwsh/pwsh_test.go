//go:build windows

package pwsh_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"testing"
	"time"

	"github.com/peersh/peersh/windows/pwsh"
)

// requirePwsh skips the test when neither pwsh.exe nor powershell.exe is on
// PATH. CI hosts vary; local Windows dev boxes always have one.
func requirePwsh(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pwsh.exe"); err == nil {
		return
	}
	if _, err := exec.LookPath("powershell.exe"); err == nil {
		return
	}
	t.Skip("no pwsh.exe or powershell.exe on PATH")
}

func TestExecGetProcessSentinel(t *testing.T) {
	requirePwsh(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h, err := pwsh.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	out, err := h.Exec(ctx, "Get-Process | Select-Object -First 5 | Out-String")
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}

	var got bytes.Buffer
	for {
		c, err := out.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		got.Write(c.Data)
	}

	if got.Len() == 0 {
		t.Fatal("expected non-empty output from Get-Process")
	}
	if bytes.Contains(got.Bytes(), []byte("__PEERSH_END_")) {
		t.Fatalf("sentinel leaked into output:\n%s", got.String())
	}
}

func TestSessionContinuity(t *testing.T) {
	requirePwsh(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	h, err := pwsh.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })

	// Set a variable in command 1, read it in command 2 — confirms a
	// single long-lived pwsh session preserves state across Exec calls,
	// which is the architectural contract of the host.
	mustExecAll(t, ctx, h, "$peersh_test = 'kept'")

	out := mustExecAll(t, ctx, h, "Write-Output $peersh_test")
	if !bytes.Contains(out, []byte("kept")) {
		t.Fatalf("variable not preserved across Exec; got %q", out)
	}
}

func mustExecAll(t *testing.T, ctx context.Context, h *pwsh.Host, cmd string) []byte {
	t.Helper()
	o, err := h.Exec(ctx, cmd)
	if err != nil {
		t.Fatalf("Exec %q: %v", cmd, err)
	}
	var buf bytes.Buffer
	for {
		c, err := o.Recv(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		buf.Write(c.Data)
	}
	return buf.Bytes()
}
