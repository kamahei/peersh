@echo off
REM Build the peersh-host MSI on Windows.
REM
REM Prerequisites:
REM   - Go 1.22+ on PATH
REM   - WiX 4 toolset on PATH (`dotnet tool install --global wix`)
REM
REM Usage:
REM   scripts\build-msi.cmd [version]
REM
REM Default version is 0.1.0; matches the placeholder in
REM windows/installer/peersh.wxs unless the caller overrides.

setlocal ENABLEDELAYEDEXPANSION

set "VERSION=%~1"
if "%VERSION%"=="" set "VERSION=0.1.0"

pushd "%~dp0..\" || exit /b 1

if not exist bin mkdir bin
if not exist dist mkdir dist

echo == build peershd.exe (amd64) ==
set "GOOS=windows"
set "GOARCH=amd64"
go build -ldflags "-s -w -X main.version=%VERSION%" -o bin\peershd.exe .\windows\cmd\peershd
if errorlevel 1 goto :fail

echo == build peersh-cli.exe (amd64) ==
go build -ldflags "-s -w -X main.version=%VERSION%" -o bin\peersh-cli.exe .\cli\cmd\peersh-cli
if errorlevel 1 goto :fail

REM WiX wants License.rtf next to the .wxs. Generate a minimal RTF
REM wrapper around plain text — the WiX UI banner just needs valid
REM RTF, not a typeset masterpiece.
echo == regenerate License.rtf from LICENSE ==
powershell -NoProfile -Command "$src = Get-Content -Raw LICENSE; $body = $src -replace '\\','\\\\' -replace '\{','\\{' -replace '\}','\\}' -replace '\r?\n','\par '; '{\rtf1\ansi\deff0 ' + $body + '}' | Set-Content -NoNewline -Encoding ASCII windows\installer\License.rtf"
if errorlevel 1 goto :fail

echo == build MSI with WiX 4 ==
where wix >nul 2>&1
if errorlevel 1 (
  echo wix CLI not found. Install with: dotnet tool install --global wix
  goto :fail
)

set "MSI=dist\peersh-host-%VERSION%-x64.msi"
wix build windows\installer\peersh.wxs ^
  -ext WixToolset.UI.wixext ^
  -d Version=%VERSION% ^
  -d BinDir=%CD%\bin ^
  -d LicenseRtf=%CD%\windows\installer\License.rtf ^
  -arch x64 ^
  -o %MSI%
if errorlevel 1 goto :fail

echo.
echo Built %MSI%.
echo Verify with:
echo   msiexec /i %MSI% /l*v dist\install.log
echo.
exit /b 0

:fail
echo.
echo build-msi: FAILED.
exit /b 1
