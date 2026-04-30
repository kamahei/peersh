@echo off
REM Regenerate Go code from proto/. Requires buf and protoc-gen-go on PATH.
pushd "%~dp0..\proto" || exit /b 1
buf generate
set RC=%ERRORLEVEL%
popd
exit /b %RC%
