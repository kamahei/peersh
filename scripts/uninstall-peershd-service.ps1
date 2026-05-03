# Uninstall the peershd Windows service.
# Run from an elevated PowerShell.

[CmdletBinding()]
param([string]$Name = "peershd")

$ErrorActionPreference = "Stop"

if (-not (([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltinRole]::Administrator))) {
  throw "This script must be run from an elevated PowerShell session."
}

$svc = Get-Service -Name $Name -ErrorAction SilentlyContinue
if (-not $svc) {
  Write-Host "Service '$Name' is not installed."
  return
}

if ($svc.Status -eq "Running") {
  Write-Host "Stopping service..."
  Stop-Service -Name $Name -Force
}

& sc.exe delete $Name | Out-Null
Write-Host "Service '$Name' removed."
