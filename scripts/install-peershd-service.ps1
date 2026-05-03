# Install peershd as a Windows service running as LocalSystem.
#
# Service mode is for "always on, before any user logs in" deployments.
# LocalSystem cannot read the install user's %LOCALAPPDATA%, so the
# Firebase refresh token must live somewhere LocalSystem can read --
# the install dir under Program Files works (ACLs already restrict it
# to SYSTEM + Administrators).

[CmdletBinding()]
param(
  [Parameter(Mandatory = $true)]
  [string]$Binary,

  [Parameter(Mandatory = $true)]
  [string]$InstallDir,

  [Parameter(Mandatory = $true)]
  [string]$TokenFile,

  [string]$Name = "peershd",
  [string]$DisplayName = "peersh host daemon",
  [string]$Description = "peersh host daemon - accepts incoming QUIC sessions from peersh mobile / CLI clients."
)

$ErrorActionPreference = "Stop"

if (-not (([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator))) {
  throw "This script must be run from an elevated PowerShell session."
}

if (-not (Test-Path -LiteralPath $Binary)) { throw "Binary not found: $Binary" }
$Binary = (Resolve-Path -LiteralPath $Binary).Path

if (-not (Test-Path -LiteralPath $InstallDir)) {
  New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
}
$InstallDir = (Resolve-Path -LiteralPath $InstallDir).Path
$logDir = Join-Path $InstallDir "logs"
New-Item -ItemType Directory -Force -Path $logDir | Out-Null

# One-shot Firebase OAuth as the installing admin user. Distrib builds
# with embedded Firebase config will open a browser; non-Firebase
# builds exit non-zero and we proceed without persisting a token.
Write-Host "Attempting Firebase one-shot sign-in (browser may open)..."
$loginArgs = @("-firebase-login", "-firebase-login-only", "-firebase-token-file", $TokenFile)
$proc = Start-Process -FilePath $Binary -ArgumentList $loginArgs -NoNewWindow -PassThru -Wait
if ($proc.ExitCode -eq 0) {
  Write-Host "Firebase sign-in OK; refresh token at $TokenFile"
  # Lock down the token file: SYSTEM + Administrators only.
  & icacls.exe $TokenFile /inheritance:r 2>$null | Out-Null
  & icacls.exe $TokenFile /grant:r "SYSTEM:F" "Administrators:F" 2>$null | Out-Null
} else {
  Write-Warning "Firebase login skipped/failed (exit $($proc.ExitCode)); assuming PSK or already paired."
}

$existing = Get-Service -Name $Name -ErrorAction SilentlyContinue
if ($existing) {
  Write-Host "Service '$Name' already exists; stopping and removing first..."
  if ($existing.Status -eq "Running") { Stop-Service -Name $Name -Force }
  & sc.exe delete $Name | Out-Null
  Start-Sleep -Seconds 1
}

# Build the service binPath. Always include -firebase-token-file so
# LocalSystem doesn't fall back to its own (empty) %LOCALAPPDATA%.
$binPathArgs = "-firebase-token-file `"$TokenFile`""
$cmdline = "`"$Binary`" $binPathArgs"

Write-Host "Creating service '$Name' -> $cmdline"
& sc.exe create $Name binPath= $cmdline DisplayName= $DisplayName start= auto | Out-Null
& sc.exe description $Name $Description | Out-Null

# Recovery: restart on failure, twice, then leave it.
& sc.exe failure $Name reset= 86400 actions= restart/5000/restart/15000/`"`" | Out-Null

Write-Host "Service installed. Start with: Start-Service $Name"
