@echo off
REM Stop and remove the peershd Windows service. The persisted refresh
REM token at C:\ProgramData\peersh\firebase-refresh-token.txt is left
REM in place; delete the file manually if you want to revoke this
REM device's Firebase identity.
REM
REM Run as Administrator.

setlocal EnableExtensions

set "SERVICE_NAME=peershd"

net session >nul 2>&1
if errorlevel 1 (
  echo [error] this script must be run as Administrator.
  exit /b 1
)

sc query "%SERVICE_NAME%" >nul 2>&1
if errorlevel 1 (
  echo [uninstall-service] service "%SERVICE_NAME%" not found; nothing to do.
  exit /b 0
)

sc stop "%SERVICE_NAME%" >nul 2>&1
sc delete "%SERVICE_NAME%"
if errorlevel 1 (
  echo [uninstall-service] sc delete failed.
  exit /b 1
)

echo [uninstall-service] service "%SERVICE_NAME%" removed.
endlocal
exit /b 0
