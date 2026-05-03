@echo off
setlocal

set "TASK_NAME=peershd-logon"
set "INSTALL_DIR=%LOCALAPPDATA%\peersh"
set "SCRIPT_DIR=%~dp0"

for %%I in ("%SCRIPT_DIR%..") do set "PEERSH_DIR=%%~fI"

if "%~1"=="/?" goto usage
if "%~1"=="-h" goto usage
if "%~1"=="--help" goto usage

if not "%~2"=="" (
  echo Unexpected argument: "%~2"
  goto usage_error
)

if "%LOCALAPPDATA%"=="" (
  echo LOCALAPPDATA is not set. Run this from an interactive user session.
  exit /b 1
)

if "%~1"=="" (
  set "SOURCE_BINARY=%PEERSH_DIR%\local\peershd.exe"
) else (
  set "SOURCE_BINARY=%~1"
)

for %%I in ("%SOURCE_BINARY%") do set "SOURCE_BINARY=%%~fI"

set "TARGET_BINARY=%INSTALL_DIR%\peershd.exe"
set "INSTALL_SCRIPT=%SCRIPT_DIR%install-peershd-task.ps1"
set "UNINSTALL_SCRIPT=%SCRIPT_DIR%uninstall-peershd-task.ps1"

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

if not exist "%UNINSTALL_SCRIPT%" (
  echo Uninstall script not found: %UNINSTALL_SCRIPT%
  exit /b 1
)

if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
if errorlevel 1 exit /b 1

echo Stopping existing %TASK_NAME% task, if present...
%PS_EXE% -NoProfile -ExecutionPolicy Bypass -File "%UNINSTALL_SCRIPT%" -TaskName "%TASK_NAME%" -InstallDir "%INSTALL_DIR%"
if errorlevel 1 exit /b 1

echo Copying peershd to %INSTALL_DIR%...
copy /Y "%SOURCE_BINARY%" "%TARGET_BINARY%" >nul
if errorlevel 1 exit /b 1

echo Registering %TASK_NAME% logon task for the current user...
%PS_EXE% -NoProfile -ExecutionPolicy Bypass -File "%INSTALL_SCRIPT%" -Binary "%TARGET_BINARY%" -InstallDir "%INSTALL_DIR%" -TaskName "%TASK_NAME%"
if errorlevel 1 exit /b 1

echo Done.
echo peershd runs hidden at logon. Logs are written to:
echo   %INSTALL_DIR%\logs\peershd.log
exit /b 0

:usage
echo Usage: %~nx0 [path-to-peershd.exe]
echo.
echo If no exe path is provided, this script uses:
echo   %PEERSH_DIR%\local\peershd.exe
echo.
echo Files are installed under:
echo   %LOCALAPPDATA%\peersh
echo.
echo Firebase mode: a Google sign-in browser window opens once during
echo install. The persisted refresh token in the install dir is reused
echo on every subsequent logon.
echo.
echo PSK mode: peershd refuses one-shot login (no Firebase config),
echo the script detects that and skips the auth step.
exit /b 0

:usage_error
echo Usage: %~nx0 [path-to-peershd.exe]
exit /b 1
