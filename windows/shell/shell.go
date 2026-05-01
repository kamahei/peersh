// Package shell resolves a logical shell name to an absolute executable path
// plus the argument vector needed to install peersh's prompt instrumentation.
//
// peersh wraps every interactive shell with an OSC 9;9 prompt emitter so the
// host can track the session's current working directory by parsing the
// output stream (see windows/session/cwdtracker — Tier 2 of Phase 8).
//
// PowerShell's wrapper is delivered via -EncodedCommand (base64 of UTF-16-LE
// PowerShell source) so the command line is independent of the host's quoting
// rules. The wrapper composes on top of the user's $PROFILE prompt, it does
// NOT clobber it: the original prompt block is captured before redefinition
// and invoked unchanged underneath the OSC sequence.
//
// cmd.exe gets the same effect via the legacy PROMPT macros: $E (ESC) + the
// OSC body + $E\\ (ESC + '\\' = ST terminator). /D is passed to suppress
// HKCU\\Software\\Microsoft\\Command Processor\\AutoRun, which would
// otherwise let a user-owned registry value clobber our prompt before we
// install it.
//
// Unknown shell names are rejected so a misconfigured peershd cannot be
// abused as a generic process launcher.
package shell

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf16"
)

// Resolved is the output of Resolve: an absolute path plus the args vector
// to start the shell with the OSC 9;9 prompt wrapper installed.
type Resolved struct {
	Path string
	Args []string
}

// Resolve maps a logical shell name to its absolute path and wrapped args.
//
// Recognized names: "", "auto" (prefer pwsh, fall back to powershell, then
// to powershell at the SystemRoot fallback path); "pwsh"; "powershell";
// "cmd". Anything else returns an error.
func Resolve(name string) (Resolved, error) {
	switch name {
	case "", "auto":
		if p, err := exec.LookPath("pwsh"); err == nil {
			return Resolved{Path: p, Args: powerShellArgs()}, nil
		}
		if p, err := exec.LookPath("powershell"); err == nil {
			return Resolved{Path: p, Args: powerShellArgs()}, nil
		}
		if runtime.GOOS == "windows" {
			candidate := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			if _, err := os.Stat(candidate); err == nil {
				return Resolved{Path: candidate, Args: powerShellArgs()}, nil
			}
		}
		return Resolved{}, errors.New("shell: no PowerShell binary on PATH")

	case "pwsh":
		if p, err := exec.LookPath("pwsh"); err == nil {
			return Resolved{Path: p, Args: powerShellArgs()}, nil
		}
		return Resolved{}, errors.New("shell: pwsh not found on PATH")

	case "powershell":
		if p, err := exec.LookPath("powershell"); err == nil {
			return Resolved{Path: p, Args: powerShellArgs()}, nil
		}
		if runtime.GOOS == "windows" {
			candidate := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
			if _, err := os.Stat(candidate); err == nil {
				return Resolved{Path: candidate, Args: powerShellArgs()}, nil
			}
		}
		return Resolved{}, errors.New("shell: powershell not found")

	case "cmd":
		if runtime.GOOS != "windows" {
			return Resolved{}, errors.New("shell: cmd requires Windows")
		}
		candidate := filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
		if _, err := os.Stat(candidate); err == nil {
			return Resolved{Path: candidate, Args: cmdArgs()}, nil
		}
		return Resolved{}, errors.New("shell: cmd.exe not found at expected location")

	default:
		return Resolved{}, fmt.Errorf("shell: unknown shell %q (allowed: pwsh, powershell, cmd, auto)", name)
	}
}

// PowerShellWrapperScript returns the raw PowerShell source that, once
// executed, redefines `prompt` to emit OSC 9;9 followed by the user's
// original prompt. Exposed for tests.
func PowerShellWrapperScript() string {
	return powerShellWrapperScript
}

const powerShellWrapperScript = `$global:_PeershPriorPrompt = (Get-Item function:prompt).ScriptBlock
function global:prompt {
  $base = & $global:_PeershPriorPrompt
  if ($PWD.Provider.Name -eq 'FileSystem') {
    "$([char]27)]9;9;$($PWD.ProviderPath)$([char]7)$base"
  } else {
    $base
  }
}
`

func powerShellArgs() []string {
	return []string{"-NoLogo", "-NoExit", "-EncodedCommand", encodePwshScript(powerShellWrapperScript)}
}

// CmdArgs returns the cmd.exe argument vector. Exposed for tests.
func CmdArgs() []string { return cmdArgs() }

func cmdArgs() []string {
	return []string{"/D", "/K", `prompt $E]9;9;$P$E\$P$G `}
}

// encodePwshScript renders a PowerShell script as the base64 of its
// UTF-16-LE encoding, the form -EncodedCommand expects.
func encodePwshScript(script string) string {
	script = strings.ReplaceAll(script, "\r\n", "\n")
	units := utf16.Encode([]rune(script))
	buf := make([]byte, len(units)*2)
	for i, u := range units {
		binary.LittleEndian.PutUint16(buf[i*2:], u)
	}
	return base64.StdEncoding.EncodeToString(buf)
}
