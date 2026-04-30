@echo off
REM Windows wrapper for the mobile-core build. iOS is skipped on Windows.
REM Prerequisites:
REM   - gomobile installed (go install golang.org/x/mobile/cmd/gomobile@latest)
REM   - gomobile init (one-time)
REM   - ANDROID_HOME / ANDROID_NDK_HOME set
setlocal
pushd "%~dp0..\" || exit /b 1
if not exist app\android\app\libs mkdir app\android\app\libs
echo ^>^> gomobile bind -target=android
gomobile bind -target=android -androidapi 21 -o app\android\app\libs\peersh.aar github.com/peersh/peersh/mobile-core
set RC=%ERRORLEVEL%
popd
exit /b %RC%
