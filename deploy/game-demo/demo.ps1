param([ValidateSet('up-data','up-app','verify','reset-data','stop','status')][string]$Action='status',[switch]$LocalBuild)
$ErrorActionPreference='Stop'
$demoRoot = $PSScriptRoot
$repoRoot = (Resolve-Path (Join-Path $demoRoot '../..')).Path
$composeFile = Join-Path $demoRoot 'compose.yml'
$isolationEnv = Join-Path $demoRoot 'empty.env'
function Compose {
    $composeArgs=@('compose','--project-name','gameshelf-6021','--env-file',$isolationEnv,'-f',$composeFile)
    if($LocalBuild){$composeArgs+=@('-f',(Join-Path $demoRoot 'compose.local.yml'))}
    & docker @composeArgs @args
    if ($LASTEXITCODE -ne 0) { throw "Demo Docker command failed ($LASTEXITCODE)." }
}
switch ($Action) {
 'status' { Compose --profile app ps --all }
 'up-data' { Compose up -d --wait game-demo-postgres }
 'up-app' { Compose --profile app up -d --build --wait }
 'verify' {
    Compose exec -T game-demo-postgres psql -U game_demo_admin -d gameshelf_demo -f /demo/oracle.sql
    if($LocalBuild){
      $testBinary=Join-Path $repoRoot '.agent-runs/game-demo/services.test'
      New-Item -ItemType Directory -Path (Split-Path $testBinary) -Force | Out-Null
      $oldGOOS=$env:GOOS;$oldGOARCH=$env:GOARCH;$oldCGO=$env:CGO_ENABLED
      Push-Location (Join-Path $repoRoot 'backend')
      try {
        $env:GOOS='linux';$env:GOARCH='amd64';$env:CGO_ENABLED='0'
        & go test ./internal/services -c -o $testBinary
        if($LASTEXITCODE -ne 0){throw 'Linux integration test compilation failed'}
      } finally {Pop-Location;$env:GOOS=$oldGOOS;$env:GOARCH=$oldGOARCH;$env:CGO_ENABLED=$oldCGO}
      Compose --profile test run --rm --no-deps game-demo-tests-local
    } else { Compose --profile test run --rm --no-deps game-demo-tests }
 }
 'reset-data' {
    # This resets only the marked synthetic database, never application history.
    Compose --profile app stop game-demo-backend game-demo-client
    Compose exec -T game-demo-postgres psql -U game_demo_admin -d gameshelf_demo -f /docker-entrypoint-initdb.d/10-seed.sql
    Write-Output 'Synthetic business data reset. Run -Action up-app to restart the demo application.'
 }
 'stop' { Compose --profile app stop }
}
