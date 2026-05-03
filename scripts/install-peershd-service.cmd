@echo off
REM Register peershd as a Windows service (LocalSystem) so it starts at
REM boot, before any user logs on. Requires elevated privileges.
REM
REM Usage (run as Administrator):
REM   install-peershd-service.cmd                       (uses ..\local\peershd.exe)
REM   install-peershd-service.cmd "C:\path\peershd.exe"
REM
REM Firebase mode: the refresh token is stored at
REM   C:\ProgramData\peersh\firebase-refresh-token.txt
REM (NOT %LOCALAPPDATA% -- the service runs as LocalSystem, which has a
REM separate profile and can't see the install-time user's appdata).
REM During install, this script opens a browser, signs you in, and
REM writes the token to that ProgramData path with ACLs locked down to
REM SYSTEM + Administrators.
REM
REM PSK mode: peershd refuses one-shot login (no Firebase config),
REM the script detects that and skips the auth step.
REM
REM Companion uninstall: uninstall-peershd-service.cmd

setlocal EnableExtensions EnableDelayedExpansion

set "SERVICE_NAME=peershd"
set "TOKEN_DIR=%ProgramData%\peersh"
set "TOKEN_FILE=%TOKEN_DIR%\firebase-refresh-token.txt"

REM Refuse to run unelevated -- sc.exe needs admin.
net session >nul 2>&1
if errorlevel 1 (
  echo [error] this script must be run as Administrator.
  exit /b 1
)

set "EXE=%~1"
if "%EXE%"=="" set "EXE=%~dp0..\local\peershd.exe"
for %%I in ("%EXE%") do set "EXE=%%~fI"

if not exist "%EXE%" (
  echo [error] peershd.exe not found at: %EXE%
  echo Pass the absolute path as the first argument, e.g.:
  echo   %~nx0 "C:\Program Files\peersh\peershd.exe"
  exit /b 1
)

echo [install-service] using peershd: %EXE%

if not exist "%TOKEN_DIR%" mkdir "%TOKEN_DIR%" >nul 2>&1
REM Lock the token directory: SYSTEM + Administrators full, no inherit.
icacls "%TOKEN_DIR%" /inheritance:r >nul 2>&1
icacls "%TOKEN_DIR%" /grant:r "SYSTEM:(OI)(CI)F" "Administrators:(OI)(CI)F" >nul 2>&1

echo [install-service] attempting Firebase one-shot sign-in (browser will open)...
"%EXE%" -firebase-login -firebase-login-only -firebase-token-file "%TOKEN_FILE%"
set "AUTH_RC=%ERRORLEVEL%"
if "%AUTH_RC%"=="0" (
  echo [install-service] Firebase sign-in OK; refresh token at %TOKEN_FILE%
) else (
  echo [install-service] Firebase login skipped/failed ^(rc=%AUTH_RC%^); assuming PSK or already paired.
)

REM sc.exe binPath uses spaces between flags so the whole thing must be
REM quoted as a single argument; embedded quotes are escaped with \".
set "BIN=\"%EXE%\""
if exist "%TOKEN_FILE%" (
  set "BIN=%BIN% -firebase-token-file \"%TOKEN_FILE%\""
)

sc query "%SERVICE_NAME%" >nul 2>&1
if not errorlevel 1 (
  echo [install-service] service "%SERVICE_NAME%" exists; deleting first...
  sc stop "%SERVICE_NAME%" >nul 2>&1
  sc delete "%SERVICE_NAME%" >nul 2>&1
  REM Brief settle so the SCM finishes the delete before the create.
  ping -n 2 127.0.0.1 >nul 2>&1
)

sc create "%SERVICE_NAME%" binPath= "%BIN%" start= auto DisplayName= "peersh host daemon"
if errorlevel 1 (
  echo [install-service] sc create failed.
  exit /b 1
)
sc description "%SERVICE_NAME%" "peersh host daemon -- accepts incoming QUIC sessions from the peersh mobile / CLI clients."

sc start "%SERVICE_NAME%"
if errorlevel 1 (
  echo [install-service] sc start failed; check the Windows Event Log.
  exit /b 1
)

echo [install-service] service "%SERVICE_NAME%" installed and started.
echo                   Logs: Event Viewer -^> Windows Logs -^> Application
endlocal
exit /b 0
