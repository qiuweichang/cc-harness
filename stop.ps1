$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$statePath = Join-Path $projectRoot '.runtime\processes.json'

# Stop-RecordedProcess terminates a recorded service together with npm/Vite children.
function Stop-RecordedProcess {
    param([Parameter(Mandatory = $true)][int]$ProcessId)

    if (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue) {
        & taskkill.exe /PID $ProcessId /T /F 2>$null | Out-Null
        Write-Host "已停止进程树 $ProcessId" -ForegroundColor Green
    }
}

if (-not (Test-Path -LiteralPath $statePath)) {
    Write-Host '没有找到 cc-hardness 进程记录。' -ForegroundColor Yellow
    exit 0
}

$state = Get-Content -LiteralPath $statePath -Raw -Encoding UTF8 | ConvertFrom-Json
$processIds = @($state.processes.backend, $state.processes.frontend) | Where-Object { $_ }

foreach ($processId in $processIds) {
    Stop-RecordedProcess -ProcessId $processId
}

Remove-Item -LiteralPath $statePath -Force
