@echo off
setlocal
set "DEMO_DIR=%~dp0"
set "DEMO_URL=http://localhost:55173"

docker compose --project-name gameshelf-6021 --env-file "%DEMO_DIR%empty.env" -f "%DEMO_DIR%compose.yml" -f "%DEMO_DIR%compose.local.yml" --profile app up -d --no-build --pull never --wait --wait-timeout 90
if errorlevel 1 goto :failed

curl.exe --noproxy "*" --fail --silent --show-error --retry 8 --retry-delay 1 --retry-connrefused --max-time 5 --output NUL http://127.0.0.1:5300/health
if errorlevel 1 goto :failed
curl.exe --noproxy "*" --fail --silent --show-error --retry 8 --retry-delay 1 --retry-connrefused --max-time 5 --output NUL http://127.0.0.1:55173/
if errorlevel 1 goto :failed

echo GameShelf is ready: %DEMO_URL%
if /i not "%~1"=="--no-browser" start "" "%DEMO_URL%"
exit /b 0

:failed
echo GameShelf did not become ready. Make sure Docker Desktop is running.
echo If demo images are missing, see "%DEMO_DIR%README.md" for the initial build.
if /i not "%~1"=="--no-browser" pause
exit /b 1
