@echo off
setlocal

set "SERVICE_NAME=peershd"
set "INSTALL_DIR=C:\Program Files\peersh"
set "SCRIPT_DIR=%~dp0"
set "TARGET_BINARY=%INSTALL_DIR%\peershd.exe"
set "TARGET_TOKEN=%INSTALL_DIR%\firebase-refresh-token.txt"
set "UNINSTALL_SCRIPT=%SCRIPT_DIR%uninstall-peershd-service.ps1"
set "REMOVE_FILES=0"

if "%~1"=="/?" goto usage
if "%~1"=="-h" goto usage
if "%~1"=="--help" goto usage

if /I "%~1"=="/remove-files" (
  set "REMOVE_FILES=1"
) else if /I "%~1"=="--remove-files" (
  set "REMOVE_FILES=1"
) else if not "%~1"=="" (
  echo Unexpected argument: "%~1"
  goto usage_error
)

if not "%~2"=="" (
  echo Unexpected argument: "%~2"
  goto usage_error
)

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

if not exist "%UNINSTALL_SCRIPT%" (
  echo Uninstall script not found: %UNINSTALL_SCRIPT%
  exit /b 1
)

echo Uninstalling %SERVICE_NAME% service...
%PS_EXE% -NoProfile -ExecutionPolicy Bypass -File "%UNINSTALL_SCRIPT%" -Name "%SERVICE_NAME%"
if errorlevel 1 exit /b 1

if "%REMOVE_FILES%"=="0" (
  echo Kept install directory: %INSTALL_DIR%
  exit /b 0
)

if exist "%TARGET_BINARY%" (
  echo Removing %TARGET_BINARY%...
  del /F /Q "%TARGET_BINARY%"
)

if exist "%TARGET_TOKEN%" (
  echo Removing %TARGET_TOKEN%...
  del /F /Q "%TARGET_TOKEN%"
)

if exist "%INSTALL_DIR%\logs" (
  rmdir /S /Q "%INSTALL_DIR%\logs"
)

rmdir "%INSTALL_DIR%" 2>nul

echo Done.
exit /b 0

:usage
echo Usage: %~nx0 [/remove-files]
echo.
echo Removes the peershd Windows service.
echo.
echo By default it keeps copied peershd.exe, refresh token, and logs at:
echo   C:\Program Files\peersh
echo Use /remove-files to remove the install directory too.
exit /b 0

:usage_error
echo Usage: %~nx0 [/remove-files]
exit /b 1
