@echo off
REM Thin Windows wrapper around scripts/generate-backup-notes.sh.
REM Requires Git Bash on PATH.

setlocal
where bash >nul 2>nul
if errorlevel 1 (
  echo ERROR: bash not on PATH. Install Git for Windows ^(includes Git Bash^) and re-run.
  exit /b 1
)

cd /d "%~dp0.."
bash scripts/generate-backup-notes.sh %*
endlocal
