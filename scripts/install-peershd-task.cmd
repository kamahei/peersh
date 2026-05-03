@echo off
REM Register peershd as a per-user logon task in Windows Task Scheduler.
REM
REM Usage:
REM   install-peershd-task.cmd                       (uses ..\local\peershd.exe)
REM   install-peershd-task.cmd "C:\path\peershd.exe"
REM
REM Firebase mode: a Google sign-in browser window opens once during
REM install. The persisted refresh token (default
REM %LOCALAPPDATA%\peersh\firebase-refresh-token.txt) is reused for
REM every subsequent logon -- no further interaction needed.
REM
REM PSK mode: peershd refuses one-shot login (no Firebase config),
REM the script detects that and skips the auth step.
REM
REM Companion uninstall: uninstall-peershd-task.cmd

setlocal EnableExtensions EnableDelayedExpansion

set "TASK_NAME=peershd"
set "EXE=%~1"
if "%EXE%"=="" set "EXE=%~dp0..\local\peershd.exe"
for %%I in ("%EXE%") do set "EXE=%%~fI"

if not exist "%EXE%" (
  echo [error] peershd.exe not found at: %EXE%
  echo Pass the absolute path as the first argument, e.g.:
  echo   %~nx0 "C:\Program Files\peersh\peershd.exe"
  exit /b 1
)

echo [install-task] using peershd: %EXE%

echo [install-task] attempting Firebase one-shot sign-in (browser will open)...
"%EXE%" -firebase-login -firebase-login-only
set "AUTH_RC=%ERRORLEVEL%"
if "%AUTH_RC%"=="0" (
  echo [install-task] Firebase sign-in OK; refresh token persisted.
) else (
  echo [install-task] Firebase login skipped/failed ^(rc=%AUTH_RC%^); assuming PSK or already paired.
)

REM Run silently on logon -- no console window. /RL HIGHEST keeps the UAC
REM token for the elevated path; /F replaces an existing task without
REM prompting.
set "RUN_CMD=\"%EXE%\""

schtasks /Create /TN "%TASK_NAME%" /TR "%RUN_CMD%" /SC ONLOGON /RL HIGHEST /F
if errorlevel 1 (
  echo [install-task] schtasks /Create failed.
  exit /b 1
)

echo [install-task] task "%TASK_NAME%" registered. It will start at next logon.
echo                You can start it now with: schtasks /Run /TN "%TASK_NAME%"
endlocal
exit /b 0
