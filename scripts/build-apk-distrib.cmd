@echo off
REM Build a distribution-ready Android release APK.
REM
REM The repo ships placeholder Firebase configs at:
REM   app\lib\firebase_options.dart        (throws UnsupportedError at runtime)
REM   app\android\app\google-services.json (project_id "peersh-firebase-placeholder")
REM   app\firebase.json                    (placeholder ids)
REM
REM Operator-specific real configs live in local\ (gitignored):
REM   local\firebase_options.dart.real
REM   local\google-services.json.real
REM   local\app-firebase.json.real
REM
REM This script swaps placeholder -> real, runs flutter build, and restores
REM the placeholders via git checkout so the secrets are never accidentally
REM committed.
REM
REM Output: app\build\app\outputs\flutter-apk\app-release.apk
REM
REM Prerequisites:
REM   - flutter installed and on PATH (>= 3.24)
REM   - JDK 17 on PATH or JAVA_HOME set
REM   - mobile-core peersh.aar built (run scripts\build-mobile-core.cmd android)
REM   - local\*.real files present
REM
REM Note: when app\android\key.properties is absent, the release build
REM falls back to the debug keystore (sideload-only; not Play Store
REM acceptable). See app\android\key.properties.example.

setlocal

cd /d "%~dp0.."

set MISSING=
if not exist "local\firebase_options.dart.real"  set MISSING=local\firebase_options.dart.real
if not exist "local\google-services.json.real"   set MISSING=local\google-services.json.real
if not exist "local\app-firebase.json.real"      set MISSING=local\app-firebase.json.real
if not "%MISSING%"=="" (
  echo ERROR: missing %MISSING% -- copy your FlutterFire output here first. 1>&2
  exit /b 1
)

echo ^>^> swapping placeholder -^> real Firebase configs
copy /Y local\firebase_options.dart.real  app\lib\firebase_options.dart        >nul
copy /Y local\google-services.json.real   app\android\app\google-services.json >nul
copy /Y local\app-firebase.json.real      app\firebase.json                    >nul

echo ^>^> flutter build apk --release
pushd app
call flutter build apk --release
set RC=%ERRORLEVEL%
popd

echo ^>^> restoring placeholder Firebase configs
git checkout -- app\lib\firebase_options.dart        >nul 2>&1
git checkout -- app\android\app\google-services.json >nul 2>&1
git checkout -- app\firebase.json                    >nul 2>&1

if not "%RC%"=="0" (
  echo flutter build failed (rc=%RC%) 1>&2
  exit /b %RC%
)

echo.
echo Built app\build\app\outputs\flutter-apk\app-release.apk
dir app\build\app\outputs\flutter-apk\app-release.apk

endlocal
