@echo off
REM Build peersh-cli locally for the current Windows machine, embedding
REM the same Firebase / signaling defaults that build-peershd-distrib.cmd
REM reads from local\peershd-build.env. End users running the resulting
REM binary won't need to pass -signaling, -firebase-project, or
REM -firebase-api-key to use Firebase mode out of the box.
REM
REM Required env vars in local\peershd-build.env (the same file peershd
REM uses; see scripts\peershd-build.env.example):
REM   PEERSH_BUILD_SIGNALING_URL          (used by both PSK and Firebase)
REM   PEERSH_BUILD_FIREBASE_PROJECT_ID    (Firebase mode)
REM   PEERSH_BUILD_FIREBASE_API_KEY       (Firebase mode)
REM   PEERSH_BUILD_FIREBASE_REGION        (Firebase mode; defaults to asia-northeast1)
REM   PEERSH_BUILD_FIREBASE_RTDB_REGION   (Firebase mode; defaults to asia-southeast1; used for host auto-discovery)
REM   PEERSH_BUILD_GOOGLE_CLIENT_ID       (Firebase mode, -firebase-login)
REM   PEERSH_BUILD_GOOGLE_CLIENT_SECRET   (Firebase mode, -firebase-login)
REM
REM Output: local\peersh-cli.exe (or local\peersh-cli when GOOS != windows).
REM
REM Optional env vars:
REM   GOOS, GOARCH  override target OS / arch (default: windows / amd64).

setlocal

if "%GOOS%"=="" set GOOS=windows
if "%GOARCH%"=="" set GOARCH=amd64

if exist "%~dp0..\local\peershd-build.env" (
  for /f "usebackq eol=# tokens=1,* delims==" %%A in ("%~dp0..\local\peershd-build.env") do (
    if not "%%A"=="" set "%%A=%%B"
  )
)

if "%PEERSH_BUILD_FIREBASE_REGION%"=="" set PEERSH_BUILD_FIREBASE_REGION=asia-northeast1
if "%PEERSH_BUILD_FIREBASE_RTDB_REGION%"=="" set PEERSH_BUILD_FIREBASE_RTDB_REGION=asia-southeast1

set LDFLAGS=-X main.embeddedSignalingURL=%PEERSH_BUILD_SIGNALING_URL%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseProjectID=%PEERSH_BUILD_FIREBASE_PROJECT_ID%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseAPIKey=%PEERSH_BUILD_FIREBASE_API_KEY%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseRegion=%PEERSH_BUILD_FIREBASE_REGION%
set LDFLAGS=%LDFLAGS% -X main.embeddedFirebaseRtdbRegion=%PEERSH_BUILD_FIREBASE_RTDB_REGION%
set LDFLAGS=%LDFLAGS% -X main.embeddedGoogleClientID=%PEERSH_BUILD_GOOGLE_CLIENT_ID%
set LDFLAGS=%LDFLAGS% -X main.embeddedGoogleClientSecret=%PEERSH_BUILD_GOOGLE_CLIENT_SECRET%

cd /d "%~dp0.."

set OUT=local\peersh-cli.exe
if not "%GOOS%"=="windows" set OUT=local\peersh-cli

go build -trimpath -ldflags "%LDFLAGS%" -o %OUT% .\cli\cmd\peersh-cli
if errorlevel 1 exit /b 1
echo Built %OUT% (GOOS=%GOOS% GOARCH=%GOARCH%, signaling=%PEERSH_BUILD_SIGNALING_URL%, firebase_project=%PEERSH_BUILD_FIREBASE_PROJECT_ID%).

endlocal
