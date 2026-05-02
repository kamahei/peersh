@echo off
REM Remove the peershd logon task created by install-peershd-task.cmd.
REM Refresh token (firebase mode) is left in place on disk; delete
REM %LOCALAPPDATA%\peersh\firebase-refresh-token.txt manually if you
REM want to revoke this device's Firebase identity.

setlocal EnableExtensions

set "TASK_NAME=peershd"

schtasks /Query /TN "%TASK_NAME%" >nul 2>&1
if errorlevel 1 (
  echo [uninstall-task] task "%TASK_NAME%" not found; nothing to do.
  exit /b 0
)

REM Stop a running instance (if any) before deleting the registration.
schtasks /End /TN "%TASK_NAME%" >nul 2>&1

schtasks /Delete /TN "%TASK_NAME%" /F
if errorlevel 1 (
  echo [uninstall-task] schtasks /Delete failed.
  exit /b 1
)

echo [uninstall-task] task "%TASK_NAME%" removed.
endlocal
exit /b 0
