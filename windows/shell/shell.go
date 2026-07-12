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
	"sync"
	"unicode/utf16"
)

// Resolved is the output of Resolve: an absolute path plus the args vector
// (and, on POSIX, env entries) to start the shell with the OSC 9;9 prompt
// wrapper installed.
type Resolved struct {
	Path string
	Args []string
	// Env is extra "KEY=VALUE" entries the child shell needs — on macOS/Linux
	// this carries ZDOTDIR pointing at the generated zsh rc that installs the
	// OSC 9;9 cwd hook. Empty on Windows (the wrapper rides the command line).
	Env []string
}

// Resolve maps a logical shell name to its absolute path and wrapped args.
//
// Recognized names: "", "auto" (prefer pwsh, fall back to powershell, then
// to powershell at the SystemRoot fallback path); "pwsh"; "powershell";
// "cmd". Anything else returns an error.
func Resolve(name string) (Resolved, error) {
	switch name {
	case "", "auto":
		// On macOS/Linux the default is the user's POSIX login shell
		// (zsh/bash/sh) — matching a locally-opened Terminal — not
		// PowerShell, even when pwsh happens to be installed.
		if runtime.GOOS != "windows" {
			return posixLoginShell()
		}
		if p, err := exec.LookPath("pwsh"); err == nil {
			return Resolved{Path: p, Args: powerShellArgs()}, nil
		}
		if p, err := exec.LookPath("powershell"); err == nil {
			return Resolved{Path: p, Args: powerShellArgs()}, nil
		}
		candidate := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		if _, err := os.Stat(candidate); err == nil {
			return Resolved{Path: candidate, Args: powerShellArgs()}, nil
		}
		return Resolved{}, errors.New("shell: no PowerShell binary on PATH")

	case "zsh", "bash", "sh":
		if runtime.GOOS == "windows" {
			return Resolved{}, fmt.Errorf("shell: %q requires a POSIX host", name)
		}
		if p, err := exec.LookPath(name); err == nil {
			return posixResolved(p), nil
		}
		return Resolved{}, fmt.Errorf("shell: %s not found on PATH", name)

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
		return Resolved{}, fmt.Errorf("shell: unknown shell %q (allowed: pwsh, powershell, cmd, zsh, bash, sh, auto)", name)
	}
}

// posixLoginShell resolves the host's default interactive shell on macOS/Linux:
// the user's $SHELL if set, else zsh, bash, or sh. Started as a login +
// interactive shell so the user's profile (PATH from /etc/zprofile +
// ~/.zprofile, aliases from ~/.zshrc, ...) loads, matching a locally-opened
// Terminal.
func posixLoginShell() (Resolved, error) {
	if sh := os.Getenv("SHELL"); sh != "" {
		if p, err := exec.LookPath(sh); err == nil {
			return posixResolved(p), nil
		}
	}
	for _, name := range []string{"zsh", "bash", "sh"} {
		if p, err := exec.LookPath(name); err == nil {
			return posixResolved(p), nil
		}
	}
	return Resolved{}, errors.New("shell: no POSIX shell (zsh/bash/sh) found on PATH")
}

// posixResolved builds the Resolved for an interactive POSIX shell, injecting
// the OSC 9;9 cwd-tracking prompt hook where the shell supports it:
//
//   - zsh:  a generated ZDOTDIR whose .zshrc sources the user's ~/.zshrc and
//           adds a precmd hook (started login + interactive).
//   - bash: a generated --rcfile that sources the user's rc and appends the
//           hook to PROMPT_COMMAND (started interactive).
//   - sh / others: no hook (sh has no reliable prompt hook); the interactive
//           terminal still works, but host-side cwd tracking / the session
//           file browser stay inert for that shell.
//
// If the hook files can't be generated the shell still starts, just without
// the hook — the terminal never depends on it.
func posixResolved(path string) Resolved {
	switch filepath.Base(path) {
	case "zsh":
		if dir, err := oscHookZDOTDIR(); err == nil {
			return Resolved{Path: path, Args: []string{"-l", "-i"}, Env: []string{"ZDOTDIR=" + dir}}
		}
		return Resolved{Path: path, Args: []string{"-l", "-i"}}
	case "bash":
		if rc, err := oscHookBashrc(); err == nil {
			return Resolved{Path: path, Args: []string{"--rcfile", rc, "-i"}}
		}
		return Resolved{Path: path, Args: []string{"-l", "-i"}}
	default: // sh, dash, ...
		return Resolved{Path: path, Args: []string{"-i"}}
	}
}

// The OSC 9;9 emitter, printed by every prompt so windows/session.CWDTracker
// can parse the shell's cwd out of the output stream. `\e` (ESC) and `\a`
// (BEL) are understood by both bash's and zsh's printf builtins.
const oscHookEmitter = `_peersh_osc99() { printf '\e]9;9;%s\a' "$PWD"; }`

var (
	oscHookOnce   sync.Once
	oscHookDir    string // generated ZDOTDIR for zsh
	oscHookBashRC string // generated --rcfile for bash
	oscHookErr    error
)

func oscHookInit() {
	dir, err := os.MkdirTemp("", "peersh-shell-")
	if err != nil {
		oscHookErr = err
		return
	}
	// zsh: mirror the three user rc files so the user's environment/config is
	// preserved, and add the precmd hook in .zshrc.
	files := map[string]string{
		".zshenv":   `[[ -f "${HOME}/.zshenv" ]] && source "${HOME}/.zshenv"` + "\n",
		".zprofile": `[[ -f "${HOME}/.zprofile" ]] && source "${HOME}/.zprofile"` + "\n",
		".zshrc": `[[ -f "${HOME}/.zshrc" ]] && source "${HOME}/.zshrc"` + "\n" +
			oscHookEmitter + "\n" +
			`autoload -Uz add-zsh-hook 2>/dev/null && add-zsh-hook precmd _peersh_osc99 || precmd_functions+=(_peersh_osc99)` + "\n",
		// bash: source the user's login + interactive rc, then chain the hook
		// onto PROMPT_COMMAND (idempotently).
		"bashrc": `[[ -f "${HOME}/.bash_profile" ]] && source "${HOME}/.bash_profile"` + "\n" +
			`[[ -f "${HOME}/.bashrc" ]] && source "${HOME}/.bashrc"` + "\n" +
			oscHookEmitter + "\n" +
			`case ";${PROMPT_COMMAND};" in *";_peersh_osc99;"*) ;; *) PROMPT_COMMAND="_peersh_osc99${PROMPT_COMMAND:+;${PROMPT_COMMAND}}";; esac` + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			oscHookErr = err
			return
		}
	}
	oscHookDir = dir
	oscHookBashRC = filepath.Join(dir, "bashrc")
}

func oscHookZDOTDIR() (string, error) { oscHookOnce.Do(oscHookInit); return oscHookDir, oscHookErr }
func oscHookBashrc() (string, error)  { oscHookOnce.Do(oscHookInit); return oscHookBashRC, oscHookErr }

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
