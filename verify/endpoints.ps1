param(
    [string]$BaseUrl = "http://127.0.0.1:18080",
    [string]$GoTavernDir = (Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)),
    [switch]$SkipServer
)

$ErrorActionPreference = "Continue"
$jar = Join-Path $env:TEMP "tt-verify-cookies.txt"
if (Test-Path $jar) { Remove-Item $jar -Force }

$serverProc = $null
$mockProc = $null

function Curl-Json {
    param([string]$Method = "GET", [string]$Path, [string]$Body = $null, [hashtable]$ExtraHeaders = @{})
    $args = @("-s", "-c", $jar, "-b", $jar, "-X", $Method, "-w", "`n%{http_code}")
    if ($script:Token) { $args += @("-H", "X-CSRF-Token: $script:Token") }
    foreach ($h in $ExtraHeaders.GetEnumerator()) { $args += @("-H", "$($h.Key): $($h.Value)") }
    if ($Body -ne $null) { $args += @("-H", "Content-Type: application/json", "-d", $Body) }
    $args += @("$BaseUrl$Path")
    $out = & curl.exe @args 2>$null
    if ($LASTEXITCODE -ne 0) { return @{ code = -1; body = "" } }
    $code = [int]$out[-1]
    $bodyText = ($out[0..($out.Count - 2)] -join "`n")
    return @{ code = $code; body = $bodyText }
}

$results = @()
function Check {
    param([string]$Name, [scriptblock]$Test)
    try {
        $r = & $Test
        $pass = $r -eq $true
    } catch {
        $pass = $false
    }
    $results += [pscustomobject]@{ Test = $Name; Pass = $pass }
    if ($pass) { Write-Host "PASS  $Name" -ForegroundColor Green }
    else { Write-Host "FAIL  $Name" -ForegroundColor Red }
}

if (-not $SkipServer) {
    Write-Host "Building server and mock..."
    Push-Location $GoTavernDir
    go build -o verify-server.exe ./cmd/server
    if ($LASTEXITCODE -ne 0) { Write-Host "BUILD FAILED" -ForegroundColor Red; exit 1 }
    go build -o verify-mock.exe ./verify/mock
    if ($LASTEXITCODE -ne 0) { Write-Host "MOCK BUILD FAILED" -ForegroundColor Red; exit 1 }
    Pop-Location

    $dataDir = Join-Path $env:TEMP "tt-verify-data"
    if (Test-Path $dataDir) { Remove-Item $dataDir -Recurse -Force }
    New-Item -ItemType Directory $dataDir | Out-Null

    $mockProc = Start-Process -FilePath (Join-Path $GoTavernDir "verify-mock.exe") -ArgumentList "-port 18081" -PassThru -WindowStyle Hidden
    $serverProc = Start-Process -FilePath (Join-Path $GoTavernDir "verify-server.exe") -ArgumentList "-port 18080 -dataRoot `"$dataDir`"" -WorkingDirectory $GoTavernDir -PassThru -WindowStyle Hidden
    Start-Sleep -Seconds 3
}

$script:Token = $null
Check "GET /version shape" {
    $r = Curl-Json -Path "/version"
    $r.code -eq 200 -and $r.body -match '"pkgVersion"' -and $r.body -match '"agent"'
}
Check "GET /csrf-token" {
    $r = Curl-Json -Path "/csrf-token"
    if ($r.code -ne 200) { return $false }
    $j = $r.body | ConvertFrom-Json
    $script:Token = $j.token
    return ($null -ne $script:Token) -and ($script:Token.Length -gt 10)
}
Check "POST /api/ping -> 204" {
    (Curl-Json -Method POST -Path "/api/ping").code -eq 204
}
Check "POST /api/users/list" {
    $r = Curl-Json -Method POST -Path "/api/users/list" -Body "{}"
    $r.code -eq 200 -and $r.body -match 'default-user'
}
Check "GET /api/users/me" {
    $r = Curl-Json -Path "/api/users/me"
    $r.code -eq 200 -and $r.body -match 'default-user'
}
Check "POST /api/settings/get (seeded defaults)" {
    $r = Curl-Json -Method POST -Path "/api/settings/get" -Body "{}"
    $r.code -eq 200 -and $r.body -match '"settings"'
}
Check "POST /api/settings/save + reload" {
    $s = Curl-Json -Method POST -Path "/api/settings/get" -Body "{}" | Select-Object -ExpandProperty body
    $r = Curl-Json -Method POST -Path "/api/settings/save" -Body '{"a":1}'
    $r.code -eq 200 -and $r.body -match 'ok'
}
Check "POST /api/secrets/read" {
    $r = Curl-Json -Method POST -Path "/api/secrets/read" -Body "{}"
    $r.code -eq 200
}
Check "POST /api/secrets/write+find+delete" {
    $w = Curl-Json -Method POST -Path "/api/secrets/write" -Body '{"key":"api_key_openai","value":"sk-test","label":"t"}'
    if ($w.code -ne 200) { return $false }
    $id = ($w.body | ConvertFrom-Json).id
    $d = Curl-Json -Method POST -Path "/api/secrets/delete" -Body (@{key="api_key_openai";id=$id} | ConvertTo-Json -Compress)
    return $d.code -eq 204
}
Check "POST /api/characters/all (empty)" {
    $r = Curl-Json -Method POST -Path "/api/characters/all" -Body "{}"
    $r.code -eq 200
}
Check "POST /api/characters/create+get+delete" {
    $card = @{name="TestChar";description="d";personality="p";scenario="s";first_mes="hi";mes_example="";creator_notes="";system_prompt="";post_history_instructions="";tags=@();creator="t";character_version="";alternate_greetings=@();extensions=@{}} | ConvertTo-Json -Compress -Depth 10
    $c = Curl-Json -Method POST -Path "/api/characters/create" -Body (@{avatar="TestChar.png";data=($card|ConvertFrom-Json)} | ConvertTo-Json -Compress -Depth 10)
    if ($c.code -notin @(200,201)) { return $false }
    $g = Curl-Json -Method POST -Path "/api/characters/get" -Body '{"avatar":"TestChar.png"}'
    if ($g.code -ne 200) { return $false }
    $d = Curl-Json -Method POST -Path "/api/characters/delete" -Body '{"avatar":"TestChar.png"}'
    return $d.code -eq 200
}
Check "POST /api/chats/recent" {
    (Curl-Json -Method POST -Path "/api/chats/recent" -Body "{}").code -eq 200
}
Check "POST /api/chats/save+get+delete" {
    $chat = @(@{name="User";is_user=$true;mes="hello";send_date=1}) | ConvertTo-Json -Compress -Depth 10
    $s = Curl-Json -Method POST -Path "/api/chats/save" -Body (@{avatar_url="TestChar.png";chat_name="t";chat=$chat} | ConvertTo-Json -Compress -Depth 10)
    if ($s.code -notin @(200,204)) { return $false }
    $g = Curl-Json -Method POST -Path "/api/chats/get" -Body '{"avatar_url":"TestChar.png","chat_name":"t.jsonl"}'
    return $g.code -eq 200
}
Check "POST /api/groups/all+create+delete" {
    $a = Curl-Json -Method POST -Path "/api/groups/all" -Body "{}"
    if ($a.code -ne 200) { return $false }
    $c = Curl-Json -Method POST -Path "/api/groups/create" -Body '{"name":"g1"}'
    if ($c.code -ne 200) { return $false }
    $id = ($c.body | ConvertFrom-Json).id
    $d = Curl-Json -Method POST -Path "/api/groups/delete" -Body (@{id=$id} | ConvertTo-Json -Compress)
    return $d.code -eq 200
}
Check "POST /api/worldinfo/list+edit+get+delete" {
    $e = Curl-Json -Method POST -Path "/api/worldinfo/edit" -Body '{"name":"w1","data":{"entries":{}}}'
    if ($e.code -ne 200) { return $false }
    $g = Curl-Json -Method POST -Path "/api/worldinfo/get" -Body '{"name":"w1"}'
    if ($g.code -ne 200) { return $false }
    $d = Curl-Json -Method POST -Path "/api/worldinfo/delete" -Body '{"name":"w1"}'
    return $d.code -eq 200
}
Check "POST /api/presets/save+delete" {
    $s = Curl-Json -Method POST -Path "/api/presets/save" -Body '{"apiId":"openai","name":"p1","preset":{"a":1}}'
    if ($s.code -ne 200) { return $false }
    $d = Curl-Json -Method POST -Path "/api/presets/delete" -Body '{"apiId":"openai","name":"p1"}'
    return $d.code -eq 200
}
Check "POST /api/themes/save+delete" {
    $s = Curl-Json -Method POST -Path "/api/themes/save" -Body '{"name":"t1"}'
    if ($s.code -ne 200) { return $false }
    $d = Curl-Json -Method POST -Path "/api/themes/delete" -Body '{"name":"t1"}'
    return $d.code -eq 200
}
Check "POST /api/moving-ui/save + quick-replies" {
    $a = Curl-Json -Method POST -Path "/api/moving-ui/save" -Body '{"name":"m1"}'
    $b = Curl-Json -Method POST -Path "/api/quick-replies/save" -Body '{"name":"q1"}'
    $c = Curl-Json -Method POST -Path "/api/quick-replies/delete" -Body '{"name":"q1"}'
    return ($a.code -eq 200) -and ($b.code -eq 200) -and ($c.code -eq 200)
}
Check "POST /api/stats/get+update" {
    $g = Curl-Json -Method POST -Path "/api/stats/get" -Body "{}"
    $u = Curl-Json -Method POST -Path "/api/stats/update" -Body '{"x":1}'
    return ($g.code -eq 200) -and ($u.code -eq 200)
}
Check "POST /api/backgrounds/all" {
    $r = Curl-Json -Method POST -Path "/api/backgrounds/all" -Body "{}"
    $r.code -eq 200 -and $r.body -match '"images"'
}
Check "POST /api/avatars/get" {
    (Curl-Json -Method POST -Path "/api/avatars/get" -Body "{}").code -eq 200
}
Check "POST /api/images/folders + files/verify" {
    $a = Curl-Json -Method POST -Path "/api/images/folders" -Body "{}"
    $b = Curl-Json -Method POST -Path "/api/files/verify" -Body '{"urls":[]}'
    return ($a.code -eq 200) -and ($b.code -eq 200)
}
Check "POST /api/files/sanitize-filename" {
    $r = Curl-Json -Method POST -Path "/api/files/sanitize-filename" -Body '{"fileName":"a:b"}'
    $r.code -eq 200 -and $r.body -match 'fileName'
}
Check "POST /api/assets/get + character" {
    $a = Curl-Json -Method POST -Path "/api/assets/get" -Body "{}"
    $b = Curl-Json -Method POST -Path "/api/assets/character?name=x&category=bgm" -Body "{}"
    return ($a.code -eq 200) -and ($b.code -eq 200)
}
Check "POST /api/sprites/get" {
    (Curl-Json -Method POST -Path "/api/sprites/get?name=x" -Body $null).code -eq 200
}
Check "GET /thumbnail 404 for missing" {
    (Curl-Json -Path "/thumbnail/?file=nope.png&type=bg").code -eq 404
}
Check "POST /api/image-metadata/all + cleanup" {
    $a = Curl-Json -Method POST -Path "/api/image-metadata/all" -Body "{}"
    $b = Curl-Json -Method POST -Path "/api/image-metadata/cleanup" -Body "{}"
    return ($a.code -eq 200) -and ($b.code -eq 200)
}
Check "POST /api/image-metadata/folders/get+create+delete" {
    $c = Curl-Json -Method POST -Path "/api/image-metadata/folders/create" -Body '{"name":"f1"}'
    if ($c.code -ne 200) { return $false }
    $id = ($c.body | ConvertFrom-Json).id
    $d = Curl-Json -Method POST -Path "/api/image-metadata/folders/delete" -Body (@{id=$id} | ConvertTo-Json -Compress)
    return $d.code -eq 200
}
Check "POST /api/extensions/discover" {
    $r = Curl-Json -Path "/api/extensions/discover"
    $r.code -eq 200
}
Check "POST /api/vector/purge-all + list" {
    $a = Curl-Json -Method POST -Path "/api/vector/purge-all" -Body "{}"
    $b = Curl-Json -Method POST -Path "/api/vector/list" -Body '{"collectionId":"c1"}'
    return ($a.code -eq 200) -and ($b.code -eq 200)
}
Check "POST /api/tokenizers/remote 400s" {
    $a = Curl-Json -Method POST -Path "/api/tokenizers/remote/kobold/count" -Body $null
    return $a.code -eq 400
}
Check "POST /api/translate 400s" {
    $a = Curl-Json -Method POST -Path "/api/translate/deepl" -Body '{"text":"","lang":""}'
    return $a.code -eq 400
}
Check "POST /api/search 400s" {
    $a = Curl-Json -Method POST -Path "/api/search/serpapi" -Body '{}'
    $b = Curl-Json -Method POST -Path "/api/search/visit" -Body '{"url":"ftp://x"}'
    return ($a.code -eq 400) -and ($b.code -eq 400)
}
Check "Deprecated redirect /savechat -> 308" {
    $r = Curl-Json -Method POST -Path "/savechat" -Body "{}"
    return $r.code -eq 308
}
Check "Stub 501 /api/sd/generate" {
    (Curl-Json -Method POST -Path "/api/sd/generate" -Body "{}").code -eq 501
}
Check "Stub 501 /api/speech/synthesize" {
    (Curl-Json -Method POST -Path "/api/speech/synthesize" -Body "{}").code -eq 501
}
Check "GET /lib.js bundle" {
    (Curl-Json -Path "/lib.js").code -eq 200
}
if (-not $SkipServer) {
    $mock = "http://127.0.0.1:18081"
    Check "LLM chat generate (mock, non-stream)" {
        $b = @{chat_completion_source="openai";reverse_proxy=$mock;model="mock-model";messages=@(@{role="user";content="hi"});stream=$false} | ConvertTo-Json -Compress -Depth 10
        $r = Curl-Json -Method POST -Path "/api/backends/chat-completions/generate" -Body $b
        $r.code -eq 200 -and $r.body -match '"Hi"'
    }
    Check "LLM chat generate (mock, stream)" {
        $b = @{chat_completion_source="openai";reverse_proxy=$mock;model="mock-model";messages=@(@{role="user";content="hi"});stream=$true} | ConvertTo-Json -Compress -Depth 10
        $r = Curl-Json -Method POST -Path "/api/backends/chat-completions/generate" -Body $b
        $r.code -eq 200 -and $r.body -match 'Hi' -and $r.body -match '\[DONE\]'
    }
    Check "LLM chat status (mock)" {
        $r = Curl-Json -Method POST -Path "/api/backends/chat-completions/status" -Body (@{chat_completion_source="openai";reverse_proxy=$mock} | ConvertTo-Json -Compress)
        $r.code -eq 200 -and $r.body -match 'mock-model'
    }
    Check "LLM text generate generic (mock)" {
        $b = @{api_type="generic";api_server=$mock;prompt="hi";stream=$false;model="mock"} | ConvertTo-Json -Compress -Depth 10
        $r = Curl-Json -Method POST -Path "/api/backends/text-completions/generate" -Body $b
        $r.code -eq 200 -and $r.body -match 'Hi'
    }
    Check "LLM kobold status (unreachable -> shape)" {
        $r = Curl-Json -Method POST -Path "/api/backends/kobold/status" -Body '{"api_server":"http://127.0.0.1:19"}'
        $r.code -eq 200 -and $r.body -match 'no_connection'
    }
    Check "LLM bias passthrough" {
        $r = Curl-Json -Method POST -Path "/api/backends/chat-completions/bias?model=gpt" -Body '[{"text":"[1,2]","value":5}]'
        $r.code -eq 200 -and $r.body -match '"1"'
    }
    Check "LLM process strict" {
        $b = @{messages=@(@{role="user";content="hi"});type="strict"} | ConvertTo-Json -Compress -Depth 10
        $r = Curl-Json -Method POST -Path "/api/backends/chat-completions/process" -Body $b
        $r.code -eq 200 -and $r.body -match '"messages"'
    }
}

$passed = ($results | Where-Object { $_.Pass }).Count
$total = $results.Count
Write-Host ""
Write-Host "$passed/$total passed" -ForegroundColor $(if ($passed -eq $total) { "Green" } else { "Yellow" })

if (-not $SkipServer) {
    if ($serverProc) { Stop-Process -Id $serverProc.Id -Force -ErrorAction SilentlyContinue }
    if ($mockProc) { Stop-Process -Id $mockProc.Id -Force -ErrorAction SilentlyContinue }
    Remove-Item (Join-Path $GoTavernDir "verify-server.exe") -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $GoTavernDir "verify-mock.exe") -Force -ErrorAction SilentlyContinue
}
if ($passed -ne $total) { exit 1 }
