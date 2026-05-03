@echo off
setlocal

set "TASK_NAME=peershd-logon"
set "INSTALL_DIR=%LOCALAPPDATA%\peersh"
set "SCRIPT_DIR=%~dp0"
set "UNINSTALL_SCRIPT=%SCRIPT_DIR%uninstall-peershd-task.ps1"
set "REMOVE_FILES_ARG="

if "%~1"=="/?" goto usage
if "%~1"=="-h" goto usage
if "%~1"=="--help" goto usage

if /I "%~1"=="/remove-files" (
  set "REMOVE_FILES_ARG=-RemoveFiles"
) else if /I "%~1"=="--remove-files" (
  set "REMOVE_FILES_ARG=-RemoveFiles"
) else if not "%~1"=="" (
  echo Unexpected argument: "%~1"
  goto usage_error
)

if not "%~2"=="" (
  echo Unexpected argument: "%~2"
  goto usage_error
)

if "%LOCALAPPDATA%"=="" (
  echo LOCALAPPDATA is not set. Run this from an interactive user session.
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

echo Removing %TASK_NAME% logon task...
%PS_EXE% -NoProfile -ExecutionPolicy Bypass -File "%UNINSTALL_SCRIPT%" -TaskName "%TASK_NAME%" -InstallDir "%INSTALL_DIR%" %REMOVE_FILES_ARG%
if errorlevel 1 exit /b 1

echo Done.
exit /b 0

:usage
echo Usage: %~nx0 [/remove-files]
echo.
echo Removes the current-user logon task and stops the peershd process installed at:
echo   %LOCALAPPDATA%\peersh
echo.
echo By default it keeps copied peershd.exe, refresh token, and logs.
echo Use /remove-files to remove the install directory too.
exit /b 0

:usage_error
echo Usage: %~nx0 [/remove-files]
exit /b 1
