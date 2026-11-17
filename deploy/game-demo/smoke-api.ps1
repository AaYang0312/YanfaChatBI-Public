# Creates only a demo chat in the isolated application and exercises real DB -> report.
$ErrorActionPreference='Stop'
$base='http://localhost:5300/api'
function API([string]$Method,[string]$Path,$Body=$null){
 $params=@{Method=$Method;Uri="$base$Path";ContentType='application/json';TimeoutSec=30;NoProxy=$true}
 if($script:headers){$params.Headers=$script:headers}
 if($null -ne $Body){$params.Body=($Body|ConvertTo-Json -Depth 20 -Compress)}
 $response=Invoke-RestMethod @params
 if(!$response.success){throw "Demo API failed: $Path"}
 return $response.data
}
$login=API POST '/auth/login' @{username_or_email='game-demo-presenter';password='synthetic-coursework-only'}
$script:headers=@{Authorization="Bearer $($login.access_token)"}
$chat=API POST '/chats' @{
 display_name='6021 · 合成游戏网店 Demo'
 connection=@{type='postgresql';host='game-demo-postgres';port='5432';database='gameshelf_demo';username='game_demo_reader';password='synthetic-demo-reader-only';use_ssl=$false}
 settings=@{auto_execute_query=$false;auto_analyze_results=$false}
}
$chatID=$chat.id
if(!$chatID){throw 'Chat creation returned no ID'}
$null=API POST "/chats/$chatID/connect" @{stream_id=[guid]::NewGuid().ToString()}
$request=@{schema_version=1;request_id=[guid]::NewGuid().ToString();workflow='product_analysis';basis='payment';period=@{start_date='2026-11-02';end_date_exclusive='2026-11-09';timezone='Asia/Shanghai'};scope=@{mode='all'};product=@{product_id='RE9-PS5-STD'};group_by='shop';metrics=@('quantity','sales','cost','margin','margin_rate','avg_price')}
$run=API POST "/chats/$chatID/assistant/runs" $request
$view=$null
for($i=0;$i -lt 60;$i++){
 $view=API GET "/chats/$chatID/assistant/runs/$($run.run_id)"
 if($view.status -in @('completed','failed','cancelled','interrupted')){break}
 Start-Sleep -Milliseconds 250
}
if($view.status -ne 'completed'){throw "Run did not complete: $($view|ConvertTo-Json -Depth 12 -Compress)"}
if($view.report.contract_version -ne 'game-shop-demo-v1'){throw 'Run did not use the PostgreSQL game adapter'}
$sales=($view.report.totals|Where-Object metric_id -eq sales).value
$margin=($view.report.totals|Where-Object metric_id -eq margin).value
if([decimal]$sales -ne 12000 -or [decimal]$margin -ne 4800){throw 'API totals differ from independent oracle'}
$artifactRoot=Join-Path (Resolve-Path (Join-Path $PSScriptRoot '../..')).Path '.agent-runs/game-demo'
New-Item -ItemType Directory -Path $artifactRoot -Force | Out-Null
$view|ConvertTo-Json -Depth 40|Set-Content -LiteralPath (Join-Path $artifactRoot 'api-report.json') -Encoding utf8
Write-Output "PASS: isolated login -> chat -> PostgreSQL -> stored report. Chat=$chatID Run=$($run.run_id) sales=$sales margin=$margin"
Write-Output 'The report workflow verified by this smoke test does not require a chat model.'
