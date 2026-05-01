package shell_test

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/peersh/peersh/windows/shell"
)

func TestResolveUnknownNameRejected(t *testing.T) {
	if _, err := shell.Resolve("bash"); err == nil {
		t.Fatal("Resolve(bash): want error, got nil")
	}
}

func TestPowerShellArgsContainEncodedCommand(t *testing.T) {
	r, err := shell.Resolve("auto")
	if err != nil {
		t.Skipf("no PowerShell on this host: %v", err)
	}
	args := r.Args
	if len(args) < 4 {
		t.Fatalf("args too short: %v", args)
	}
	// Last arg is the base64-encoded UTF-16-LE script.
	encoded := args[len(args)-1]
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16-LE byte count not even: %d", len(raw))
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	decoded := string(utf16.Decode(units))
	if !strings.Contains(decoded, "_PeershPriorPrompt") {
		t.Fatalf("decoded script missing wrapper sentinel:\n%s", decoded)
	}
	if !strings.Contains(decoded, "9;9;") {
		t.Fatalf("decoded script missing OSC 9;9 emitter:\n%s", decoded)
	}
}

func TestCmdArgsShape(t *testing.T) {
	a := shell.CmdArgs()
	if len(a) < 3 || a[0] != "/D" || a[1] != "/K" {
		t.Fatalf("cmd args want /D /K prompt..., got %v", a)
	}
	if !strings.Contains(a[2], `]9;9;$P`) {
		t.Fatalf("cmd PROMPT missing OSC 9;9 prefix: %q", a[2])
	}
}
