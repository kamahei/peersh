@echo off
REM Thin Windows wrapper around scripts/generate-backup-notes.sh.
REM Requires Git Bash on PATH. Walks a few standard locations to put
REM keytool on PATH before invoking bash, so the SHA fingerprint
REM detection works even when JAVA_HOME isn't already exported.

setlocal
where bash >nul 2>nul
if errorlevel 1 (
  echo ERROR: bash not on PATH. Install Git for Windows ^(includes Git Bash^) and re-run.
  exit /b 1
)

if defined JAVA_HOME if exist "%JAVA_HOME%\bin\keytool.exe" set "PATH=%JAVA_HOME%\bin;%PATH%"

REM Android Studio bundles a JBR with keytool. Try a few standard
REM install paths.
for %%P in (
  "%ProgramFiles%\Android\Android Studio\jbr\bin"
  "%ProgramFiles(x86)%\Android\Android Studio\jbr\bin"
  "%LOCALAPPDATA%\Programs\Android Studio\jbr\bin"
  "%LOCALAPPDATA%\Android\Sdk\jbr\bin"
) do (
  if exist "%%~P\keytool.exe" set "PATH=%%~P;%PATH%"
)

REM Locally-installed JDKs under %LOCALAPPDATA%\Programs\jdk-...
for /d %%P in ("%LOCALAPPDATA%\Programs\jdk-*") do (
  if exist "%%P\bin\keytool.exe" set "PATH=%%P\bin;%PATH%"
)

cd /d "%~dp0.."
bash scripts/generate-backup-notes.sh %*
endlocal
