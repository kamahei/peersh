@echo off
setlocal

set "SERVICE_NAME=peershd"
set "INSTALL_DIR=C:\Program Files\peersh"
set "SCRIPT_DIR=%~dp0"

for %%I in ("%SCRIPT_DIR%..") do set "PEERSH_DIR=%%~fI"

if "%~1"=="/?" goto usage
if "%~1"=="-h" goto usage
if "%~1"=="--help" goto usage

if not "%~2"=="" (
  echo Unexpected argument: "%~2"
  goto usage_error
)

if "%~1"=="" (
  set "SOURCE_BINARY=%PEERSH_DIR%\local\peershd.exe"
) else (
  set "SOURCE_BINARY=%~1"
)

for %%I in ("%SOURCE_BINARY%") do set "SOURCE_BINARY=%%~fI"

set "TARGET_BINARY=%INSTALL_DIR%\peershd.exe"
set "TOKEN_FILE=%INSTALL_DIR%\firebase-refresh-token.txt"
set "INSTALL_SCRIPT=%SCRIPT_DIR%install-peershd-service.ps1"

fltmc >nul 2>&1
if errorlevel 1 (
  echo This script must be run from an elevated Command Prompt or PowerShell.
  exit /b 1
)

where pwsh.exe >nul 2>&1
if errorlevel 1 (
  set "PS_EXE=powershell.exe"
) else (
  set "PS_EXE=pwsh.exe"
)

if not exist "%SOURCE_BINARY%" (
  echo peershd binary not found: %SOURCE_BINARY%
  echo Build it first with: bash scripts\build-peershd-distrib.sh
  exit /b 1
)

if not exist "%INSTALL_SCRIPT%" (
  echo Install script not found: %INSTALL_SCRIPT%
  exit /b 1
)

sc.exe query "%SERVICE_NAME%" >nul 2>&1
if not errorlevel 1 (
  echo Stopping existing service, if running...
  %PS_EXE% -NoProfile -ExecutionPolicy Bypass -Command "$svc = Get-Service -Name '%SERVICE_NAME%' -ErrorAction SilentlyContinue; if ($svc -and $svc.Status -ne 'Stopped') { Stop-Service -Name '%SERVICE_NAME%' -Force; $svc.WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30)) }"
  if errorlevel 1 exit /b 1
)

if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
if errorlevel 1 exit /b 1

echo Copying peershd to %INSTALL_DIR%...
copy /Y "%SOURCE_BINARY%" "%TARGET_BINARY%" >nul
if errorlevel 1 exit /b 1

echo Installing %SERVICE_NAME% service...
%PS_EXE% -NoProfile -ExecutionPolicy Bypass -File "%INSTALL_SCRIPT%" -Binary "%TARGET_BINARY%" -InstallDir "%INSTALL_DIR%" -TokenFile "%TOKEN_FILE%" -Name "%SERVICE_NAME%"
if errorlevel 1 exit /b 1

echo Starting %SERVICE_NAME% service...
%PS_EXE% -NoProfile -ExecutionPolicy Bypass -Command "Start-Service -Name '%SERVICE_NAME%'; Get-Service -Name '%SERVICE_NAME%'"
if errorlevel 1 exit /b 1

echo Done.
exit /b 0

:usage
echo Usage: %~nx0 [path-to-peershd.exe]
echo.
echo If no exe path is provided, this script uses:
echo   %PEERSH_DIR%\local\peershd.exe
echo.
echo Files are installed under:
echo   C:\Program Files\peersh
echo.
echo Firebase mode: a Google sign-in browser window opens once during
echo install. The persisted refresh token is reused on every service
echo start. PSK mode: peershd refuses one-shot login (no Firebase
echo config), the script detects that and skips the auth step.
exit /b 0

:usage_error
echo Usage: %~nx0 [path-to-peershd.exe]
exit /b 1
