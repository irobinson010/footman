# Footman S0-07 STT latency run for Windows (the ZenBook). Shipped inside the zip
# as run.ps1; start it with run.cmd. Nothing is installed: downloads go to
# %LOCALAPPDATA%\footman-spike and results to .\results\.
#
#   run.cmd            base.en + small.en, CPU and the iGPU (DirectML)   ~15 min
#   run.cmd -Full      adds turbo and Parakeet                           ~35 min
param(
    [switch]$Full,
    [int]$Threads = 8,
    [int]$Runs = 3
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'      # Invoke-WebRequest is ~10x faster without the progress bar
Set-Location $PSScriptRoot

$cache  = Join-Path $env:LOCALAPPDATA 'footman-spike'
$models = Join-Path $cache 'models'
$dl     = Join-Path $cache 'dl'
New-Item -ItemType Directory -Force "$models\sherpa", "$models\whispercpp", $dl, results | Out-Null

function Get-Verified($url, $sha, $file) {
    if ((Test-Path $file) -and ((Get-FileHash $file -Algorithm SHA256).Hash -eq $sha.ToUpper())) { return }
    Write-Host "  downloading $(Split-Path $file -Leaf)"
    Invoke-WebRequest -Uri $url -OutFile "$file.part"
    Move-Item -Force "$file.part" $file
    if ((Get-FileHash $file -Algorithm SHA256).Hash -ne $sha.ToUpper()) {
        Remove-Item $file
        throw "checksum mismatch for $url"
    }
}

# Pinned prebuilt whisper.cpp CPU release (whisper-cli.exe) for the whispercli engine.
$cliZip = Join-Path $dl 'whisper-bin-x64.zip'
Write-Host '[1/4] whisper.cpp prebuilt binary'
Get-Verified 'https://github.com/ggml-org/whisper.cpp/releases/download/b5907/whisper-bin-x64.zip' `
    'ecca07b277d943e9b9f4712856a144b857271a6b333104e511e1988e80db582b' $cliZip
$cliDir = Join-Path $cache 'whisper-cli'
if (-not (Test-Path $cliDir)) { Expand-Archive $cliZip $cliDir }
$cli = (Get-ChildItem $cliDir -Recurse -Filter whisper-cli.exe | Select-Object -First 1).FullName
if (-not $cli) { throw 'whisper-cli.exe not found in the release zip' }

$ids = @('whisper-base.en', 'whisper-small.en')
if ($Full) { $ids += @('whisper-turbo', 'parakeet-tdt-0.6b-v2') }

Write-Host "[2/4] models: $($ids -join ', ')"
$lock = Get-Content (Join-Path $PSScriptRoot 'models.lock') | Where-Object { $_ -and -not $_.StartsWith('#') } |
    ForEach-Object { $c = $_ -split "`t"; [pscustomobject]@{ Id = $c[0]; Engine = $c[1]; Sha = $c[2]; Dest = $c[3]; Url = $c[4] } }
foreach ($m in $lock | Where-Object { $ids -contains $_.Id }) {
    $file = Join-Path $dl (Split-Path $m.Url -Leaf)
    Get-Verified $m.Url $m.Sha $file
    if ($m.Engine -eq 'sherpa') {
        if (-not (Test-Path "$models\sherpa\$($m.Dest)")) { tar -xjf $file -C "$models\sherpa" }   # tar.exe ships with Windows 10+
    } else {
        Copy-Item -Force $file "$models\whispercpp\$($m.Dest)"
    }
}

$out = 'results\zenbook.jsonl'
Remove-Item -ErrorAction SilentlyContinue $out
.\stt-bench.exe -detect | Out-File -Encoding utf8 results\detect.json

Write-Host '[3/4] benchmarks (about 2 minutes per row; leave the laptop plugged in and idle)'
function Bench($label, [string[]]$benchArgs) {
    Write-Host "--- $label"
    & .\stt-bench.exe @benchArgs -threads $Threads -runs $Runs -label zenbook -out $out
    if ($LASTEXITCODE -ne 0) { Write-Host "    skipped (exit $LASTEXITCODE); carrying on" }
}
foreach ($id in $ids) {
    if ($lock | Where-Object { $_.Id -eq $id -and $_.Engine -eq 'sherpa' }) {
        Bench "sherpa-onnx  $id  cpu"      @('-engine', 'sherpa', '-model', $id, '-accel', 'cpu')
        Bench "sherpa-onnx  $id  directml" @('-engine', 'sherpa', '-model', $id, '-accel', 'directml')
    }
    if ($lock | Where-Object { $_.Id -eq $id -and $_.Engine -eq 'whispercpp' }) {
        Bench "whisper.cpp (prebuilt CPU)  $id  cpu" @('-engine', 'whispercli', '-whisper-cli', $cli, '-model', $id, '-accel', 'cpu')
    }
}

Write-Host '[4/4] summary'
.\stt-bench.exe -summarize $out -md results\zenbook.md
Compress-Archive -Force -Path results\zenbook.jsonl, results\zenbook.md, results\detect.json -DestinationPath zenbook-results.zip
Write-Host ''
Write-Host "Done. Send back: $PSScriptRoot\zenbook-results.zip"
