# Install peershd as a hidden per-user logon scheduled task.
#
# Per-user (no admin) on purpose: avoids storing a service password and
# lets peershd inherit the logged-on user's environment + LOCALAPPDATA.
# Firebase mode persists the refresh token under the install dir so the
# task can re-auth headlessly at every logon.

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Binary,

  [Parameter(Mandatory = $true)]
  [string]$InstallDir,

  [string]$TaskName = "peershd-logon",
  [string]$Description = "Starts peersh host daemon when the current user logs on.",
  [switch]$NoStart
)

$ErrorActionPreference = "Stop"

function Resolve-RequiredPath {
  param(
    [Parameter(Mandatory = $true)] [string]$Path,
    [Parameter(Mandatory = $true)] [string]$Label
  )
  if (-not (Test-Path -LiteralPath $Path)) {
    throw "$Label not found: $Path"
  }
  return (Resolve-Path -LiteralPath $Path).Path
}

$Binary = Resolve-RequiredPath -Path $Binary -Label "Binary"
if (-not (Test-Path -LiteralPath $InstallDir)) {
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
}
$InstallDir = (Resolve-Path -LiteralPath $InstallDir).Path
$logDir = Join-Path $InstallDir "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

$tokenFile = Join-Path $InstallDir "firebase-refresh-token.txt"

# One-shot Firebase OAuth before registering the task. Distrib builds
# with embedded Firebase config will open a browser; PSK / non-Firebase
# builds just exit non-zero and we proceed without persisting a token.
Write-Host "Attempting Firebase one-shot sign-in (browser may open)..."
$loginArgs = @("-firebase-login", "-firebase-login-only", "-firebase-token-file", $tokenFile)
$proc = Start-Process -FilePath $Binary -ArgumentList $loginArgs -NoNewWindow -PassThru -Wait
if ($proc.ExitCode -eq 0) {
  Write-Host "Firebase sign-in OK; refresh token at $tokenFile"
} else {
  Write-Warning "Firebase login skipped/failed (exit $($proc.ExitCode)); assuming PSK or already paired."
}

# Wrapper .cmd keeps the install layout self-contained: cd into install
# dir so peershd resolves relative paths (refresh token, etc.) against
# its own folder, append all stdout/stderr to logs/peershd.log.
$runScript = Join-Path $InstallDir "run-logon-task.cmd"
$runScriptContent = @'
@echo off
setlocal
cd /d "%~dp0"
if not exist "logs" mkdir "logs"
"peershd.exe" -firebase-token-file "%~dp0firebase-refresh-token.txt" >> "logs\peershd.log" 2>&1
'@
Set-Content -LiteralPath $runScript -Value $runScriptContent -Encoding ASCII

# wscript .vbs avoids the black console flash that schtasks /TR with
# cmd.exe would cause.
$launcherScript = Join-Path $InstallDir "run-logon-task.vbs"
$launcherContent = @'
Option Explicit

Dim shell, fso, scriptDir, runScript

Set shell = CreateObject("WScript.Shell")
Set fso = CreateObject("Scripting.FileSystemObject")
scriptDir = fso.GetParentFolderName(WScript.ScriptFullName)
runScript = fso.BuildPath(scriptDir, "run-logon-task.cmd")

WScript.Quit shell.Run("""" & runScript & """", 0, True)
'@
Set-Content -LiteralPath $launcherScript -Value $launcherContent -Encoding ASCII

$identity = [Security.Principal.WindowsIdentity]::GetCurrent().Name
$windowsDir = if ($env:SystemRoot) { $env:SystemRoot } else { $env:WINDIR }
if (-not $windowsDir) { throw "SystemRoot is not set." }

$wscript = Join-Path $windowsDir "System32\wscript.exe"
if (-not (Test-Path -LiteralPath $wscript)) {
  throw "wscript.exe not found: $wscript"
}

$existingTask = Get-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
if ($existingTask) {
  Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false
}

$action = New-ScheduledTaskAction -Execute $wscript -Argument "`"$launcherScript`""
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $identity
$principal = New-ScheduledTaskPrincipal -UserId $identity -LogonType Interactive -RunLevel Limited
$settings = New-ScheduledTaskSettingsSet `
  -MultipleInstances IgnoreNew `
  -StartWhenAvailable `
  -AllowStartIfOnBatteries `
  -DontStopIfGoingOnBatteries `
  -Hidden `
  -ExecutionTimeLimit (New-TimeSpan -Seconds 0)

$task = New-ScheduledTask `
  -Action $action `
  -Trigger $trigger `
  -Principal $principal `
  -Settings $settings `
  -Description $Description

Register-ScheduledTask -TaskName $TaskName -InputObject $task | Out-Null

# Refuse to start the task if a peersh service is already running --
# they'd compete for the same RTDB SSE listener slot.
$service = Get-Service -Name "peershd" -ErrorAction SilentlyContinue
if ($service -and $service.Status -eq "Running") {
  Write-Warning "The peershd Windows service is running. Stop or uninstall it before starting the logon task to avoid duplicate registrations."
  Write-Host "Registered scheduled task: $TaskName"
  exit 0
}

if (-not $NoStart) {
  Start-ScheduledTask -TaskName $TaskName
  Write-Host "Registered and started scheduled task: $TaskName"
} else {
  Write-Host "Registered scheduled task: $TaskName"
}

Write-Host "Task user: $identity"
Write-Host "Log file: $(Join-Path $logDir 'peershd.log')"
