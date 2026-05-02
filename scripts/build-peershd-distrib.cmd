@echo off
REM Build a distribution-ready peershd.exe with embedded Firebase
REM defaults. End users running the resulting binary won't need to pass
REM any -firebase-* / -google-* flags.
REM
REM Required env vars (read from `local\peershd-build.env` if present;
REM see scripts\peershd-build.env.example):
REM   PEERSH_BUILD_FIREBASE_PROJECT_ID
REM   PEERSH_BUILD_FIREBASE_API_KEY
REM   PEERSH_BUILD_FIREBASE_REGION       (optional, default asia-northeast1)
REM   PEERSH_BUILD_SIGNALING_URL         (optional)
REM   PEERSH_BUILD_GOOGLE_CLIENT_ID
REM   PEERSH_BUILD_GOOGLE_CLIENT_SECRET
REM
REM Output:
REM   local\peershd.exe

setlocal

if exist "%~dp0..\local\peershd-build.env" (
  for /f "usebackq tokens=1,* delims==" %%A in ("%~dp0..\local\peershd-build.env") do (
    if not "%%A"=="" if not "%%A:~0,1%"=="#" set "%%A=%%B"
  )
)

if "%PEERSH_BUILD_FIREBASE_REGION%"=="" set PEERSH_BUILD_FIREBASE_REGION=asia-northeast1
if "%PEERSH_BUILD_FIREBASE_RTDB_REGION%"=="" set PEERSH_BUILD_FIREBASE_RTDB_REGION=asia-southeast1
if "%PEERSH_BUILD_VERSION%"=="" set PEERSH_BUILD_VERSION=dev

set LDFLAGS=-X main.embeddedFirebaseAPIKey=%PEERSH_BUILD_FIREBASE_API_KEY%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseProjectID=%PEERSH_BUILD_FIREBASE_PROJECT_ID%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseRegion=%PEERSH_BUILD_FIREBASE_REGION%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseRtdbRegion=%PEERSH_BUILD_FIREBASE_RTDB_REGION%
set LDFLAGS=%LDFLAGS% -X main.embeddedSignalingURL=%PEERSH_BUILD_SIGNALING_URL%
set LDFLAGS=%LDFLAGS% -X main.embeddedGoogleClientID=%PEERSH_BUILD_GOOGLE_CLIENT_ID%
set LDFLAGS=%LDFLAGS% -X main.embeddedGoogleClientSecret=%PEERSH_BUILD_GOOGLE_CLIENT_SECRET%
set LDFLAGS=%LDFLAGS% -X main.embeddedVersion=%PEERSH_BUILD_VERSION%
set LDFLAGS=%LDFLAGS% -X main.embeddedUpdateRepo=%PEERSH_BUILD_UPDATE_REPO%

cd /d "%~dp0.."
set GOOS=windows
set GOARCH=amd64
go build -trimpath -ldflags "%LDFLAGS%" -o local\peershd.exe .\windows\cmd\peershd
if errorlevel 1 exit /b 1
echo Built local\peershd.exe with embedded Firebase defaults.

endlocal
