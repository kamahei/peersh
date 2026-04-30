// Package pwsh wraps a long-lived PowerShell process (`pwsh.exe -NoExit
// -Command -`, falling back to `powershell.exe`) and lets callers run commands
// against it while preserving session state (cwd, variables) across
// invocations. Command completion is detected with a per-command sentinel
// marker.
package pwsh
