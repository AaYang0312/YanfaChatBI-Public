$ErrorActionPreference='Stop'
$repoRoot=(Resolve-Path (Join-Path $PSScriptRoot '../..')).Path
$runtimeRoot=Join-Path $repoRoot '.agent-runs/game-demo'
$backendStage=Join-Path $runtimeRoot 'backend-runtime'
$clientStage=Join-Path $runtimeRoot 'client-runtime'
New-Item -ItemType Directory -Path $backendStage,$clientStage -Force | Out-Null
$oldGOOS=$env:GOOS; $oldGOARCH=$env:GOARCH; $oldCGO=$env:CGO_ENABLED
Push-Location (Join-Path $repoRoot 'backend')
try {
 $env:GOOS='linux';$env:GOARCH='amd64';$env:CGO_ENABLED='0'
 & go build -o (Join-Path $backendStage 'main') ./cmd/main.go
 if($LASTEXITCODE -ne 0){throw 'Linux backend cross-compile failed'}
} finally { Pop-Location; $env:GOOS=$oldGOOS;$env:GOARCH=$oldGOARCH;$env:CGO_ENABLED=$oldCGO }
Copy-Item -LiteralPath (Join-Path $repoRoot 'backend/config') -Destination $backendStage -Recurse -Force
Copy-Item -LiteralPath (Join-Path $repoRoot 'backend/internal/email_templates') -Destination $backendStage -Recurse -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Dockerfile.backend-local') -Destination (Join-Path $backendStage 'Dockerfile') -Force
$oldDemo=$env:VITE_GAME_DEMO;$oldAPI=$env:VITE_API_URL;$oldFrontend=$env:VITE_FRONTEND_BASE_URL;$oldEnvironment=$env:VITE_ENVIRONMENT
Push-Location (Join-Path $repoRoot 'client')
try {
 $env:VITE_GAME_DEMO='true';$env:VITE_API_URL='http://localhost:5300/api';$env:VITE_FRONTEND_BASE_URL='http://localhost:55173';$env:VITE_ENVIRONMENT='DEVELOPMENT'
 & npm run build -- --outDir (Join-Path $clientStage 'dist') --emptyOutDir
 if($LASTEXITCODE -ne 0){throw 'Demo frontend build failed'}
} finally {Pop-Location;$env:VITE_GAME_DEMO=$oldDemo;$env:VITE_API_URL=$oldAPI;$env:VITE_FRONTEND_BASE_URL=$oldFrontend;$env:VITE_ENVIRONMENT=$oldEnvironment}
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'serve.mjs') -Destination $clientStage -Force
Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'Dockerfile.client-local') -Destination (Join-Path $clientStage 'Dockerfile') -Force
Write-Output 'Staged local builds in .agent-runs/game-demo. No .env or existing application data copied.'
