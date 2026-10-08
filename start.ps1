param(
    [switch]$NoBrowser,
    [ValidateRange(1, 65535)][int]$WebPort = 5172
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$webRoot = Join-Path $projectRoot 'web'
$runtimeRoot = Join-Path $projectRoot '.runtime'
$logRoot = Join-Path $runtimeRoot 'logs'
$backendExecutable = Join-Path $runtimeRoot 'cc-hardness.exe'

# Assert-Command stops startup early with an actionable prerequisite message.
function Assert-Command {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$InstallHint
    )

    if (-not (Get-Command $Name -ErrorAction SilentlyContinue)) {
        throw "未找到 $Name。$InstallHint"
    }
}

# Test-LocalPort prevents duplicate frontend or backend processes on repeated clicks.
function Test-LocalPort {
    param([Parameter(Mandatory = $true)][int]$Port)

    return [bool](Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue)
}

# Get-LocalPortListener returns the first listening process for a local port.
# Startup uses the owning PID to distinguish this project from unrelated local
# services, avoiding the previous false-positive "already started" result.
function Get-LocalPortListener {
    param([Parameter(Mandatory = $true)][int]$Port)

    return Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
}

# Test-ManagedFrontend verifies that a listener belongs to this repository's
# Vite process. A port owned by another project must never be treated as a
# reusable cc-hardness frontend.
function Test-ManagedFrontend {
    param(
        [Parameter(Mandatory = $true)][int]$ProcessId,
        [Parameter(Mandatory = $true)][string]$ExpectedWebRoot
    )

    $processInfo = Get-CimInstance Win32_Process -Filter "ProcessId = $ProcessId" -ErrorAction SilentlyContinue
    if (-not $processInfo) {
        return $false
    }
    return ([string]$processInfo.CommandLine).IndexOf(
        [System.IO.Path]::GetFullPath($ExpectedWebRoot),
        [System.StringComparison]::OrdinalIgnoreCase
    ) -ge 0
}

# Test-BackendStale compares the managed executable with Go sources and go.mod.
# Vite reloads frontend changes automatically, but a running Go process must be
# rebuilt whenever its compiled inputs are newer than the executable.
function Test-BackendStale {
    param([Parameter(Mandatory = $true)][string]$Executable)

    if (-not (Test-Path -LiteralPath $Executable)) {
        return $true
    }

    $executableTime = (Get-Item -LiteralPath $Executable).LastWriteTimeUtc
    $goInputs = @(
        Get-ChildItem -LiteralPath (Join-Path $projectRoot 'cmd') -Recurse -File -Filter '*.go'
        Get-ChildItem -LiteralPath (Join-Path $projectRoot 'internal') -Recurse -File -Filter '*.go'
        Get-Item -LiteralPath (Join-Path $projectRoot 'go.mod')
    )
    return [bool]($goInputs | Where-Object { $_.LastWriteTimeUtc -gt $executableTime } | Select-Object -First 1)
}

# Stop-StaleManagedBackend stops only the process whose executable is the
# cc-hardness runtime binary. An unrelated service on port 8080 is never killed.
function Stop-StaleManagedBackend {
    param(
        [Parameter(Mandatory = $true)][int]$Port,
        [Parameter(Mandatory = $true)][string]$Executable
    )

    $listener = Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $listener) {
        return
    }
    $process = Get-Process -Id $listener.OwningProcess -ErrorAction SilentlyContinue
    if (-not $process -or [System.IO.Path]::GetFullPath($process.Path) -ne [System.IO.Path]::GetFullPath($Executable)) {
        throw "端口 $Port 被其他程序占用，无法安全重启 cc-hardness 后端。"
    }

    Write-Host '检测到 Go 源码已更新，正在重启旧后端…' -ForegroundColor Cyan
    Stop-ProcessTree -ProcessId $process.Id
}

# Wait-HttpReady polls a local endpoint until startup succeeds or times out.
function Wait-HttpReady {
    param(
        [Parameter(Mandatory = $true)][string]$Url,
        [Parameter(Mandatory = $true)][int]$TimeoutSeconds
    )

    $deadline = (Get-Date).AddSeconds($TimeoutSeconds)
    while ((Get-Date) -lt $deadline) {
        try {
            $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 2
            if ($response.StatusCode -ge 200 -and $response.StatusCode -lt 500) {
                return $true
            }
        } catch {
            Start-Sleep -Milliseconds 500
        }
    }
    return $false
}

# Stop-ProcessTree removes a service and any child process created by npm or Go.
function Stop-ProcessTree {
    param([Parameter(Mandatory = $true)][int]$ProcessId)

    if (Get-Process -Id $ProcessId -ErrorAction SilentlyContinue) {
        & taskkill.exe /PID $ProcessId /T /F 2>$null | Out-Null
    }
}

Assert-Command -Name 'go' -InstallHint '请安装 Go 1.24 或更高版本。'
Assert-Command -Name 'node' -InstallHint '请安装 Node.js 20 或更高版本。'
Assert-Command -Name 'npm' -InstallHint 'npm 通常随 Node.js 一起安装。'

New-Item -ItemType Directory -Force -Path $logRoot | Out-Null

if (-not (Test-Path -LiteralPath (Join-Path $webRoot 'node_modules'))) {
    Write-Host '首次启动：正在安装 Web 依赖…' -ForegroundColor Cyan
    & npm.cmd install --prefix $webRoot
    if ($LASTEXITCODE -ne 0) {
        throw 'Web 依赖安装失败，请查看上方 npm 输出。'
    }
}

$processes = [ordered]@{}
# Only processes created by this invocation are stopped when startup fails.
# Reused healthy services must remain running if another startup step fails.
$startedProcessIds = [System.Collections.Generic.List[int]]::new()

try {
$frontendListener = Get-LocalPortListener -Port $WebPort
if ($frontendListener -and -not (Test-ManagedFrontend -ProcessId $frontendListener.OwningProcess -ExpectedWebRoot $webRoot)) {
    $foreignProcess = Get-CimInstance Win32_Process -Filter "ProcessId = $($frontendListener.OwningProcess)" -ErrorAction SilentlyContinue
    $foreignCommand = if ($foreignProcess) { [string]$foreignProcess.CommandLine } else { '未知命令' }
    throw "前端端口 $WebPort 被其他项目占用（PID $($frontendListener.OwningProcess)：$foreignCommand）。请关闭该程序，或使用 .\start.ps1 -WebPort <其他端口> 启动。"
}

$backendPortInUse = Test-LocalPort -Port 8080
if ($backendPortInUse -and (Test-BackendStale -Executable $backendExecutable)) {
    Stop-StaleManagedBackend -Port 8080 -Executable $backendExecutable
    $backendPortInUse = $false
}

if (-not $backendPortInUse) {
    Write-Host '正在准备 Go 后端…' -ForegroundColor Cyan
    & go.exe build -o $backendExecutable ./cmd/server
    if ($LASTEXITCODE -ne 0) {
        throw 'Go 后端构建失败，请查看上方输出。'
    }
    Write-Host '正在启动 Go 后端…' -ForegroundColor Cyan
    $backend = Start-Process -FilePath $backendExecutable `
        -WorkingDirectory $projectRoot `
        -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $logRoot 'backend.out.log') `
        -RedirectStandardError (Join-Path $logRoot 'backend.error.log') `
        -PassThru
    $processes['backend'] = $backend.Id
    $startedProcessIds.Add($backend.Id)
} else {
    $backendListener = Get-LocalPortListener -Port 8080
    $backendProcess = if ($backendListener) { Get-Process -Id $backendListener.OwningProcess -ErrorAction SilentlyContinue } else { $null }
    if (-not $backendProcess -or [System.IO.Path]::GetFullPath($backendProcess.Path) -ne [System.IO.Path]::GetFullPath($backendExecutable)) {
        throw '后端端口 8080 被其他程序占用，无法安全复用。'
    }
    $processes['backend'] = $backendProcess.Id
    Write-Host '后端端口 8080 已在监听，跳过重复启动。' -ForegroundColor Yellow
}

if (-not $frontendListener) {
    Write-Host '正在启动 React 前端…' -ForegroundColor Cyan
    $frontend = Start-Process -FilePath 'npm.cmd' `
        -ArgumentList @('run', 'dev', '--', '--host', '127.0.0.1', '--port', "$WebPort", '--strictPort') `
        -WorkingDirectory $webRoot `
        -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $logRoot 'frontend.out.log') `
        -RedirectStandardError (Join-Path $logRoot 'frontend.error.log') `
        -PassThru
    $processes['frontend'] = $frontend.Id
    $startedProcessIds.Add($frontend.Id)
} else {
    $processes['frontend'] = $frontendListener.OwningProcess
    Write-Host "前端端口 $WebPort 已由 cc-hardness 监听，跳过重复启动。" -ForegroundColor Yellow
}

$processState = [ordered]@{
    startedAt = (Get-Date).ToUniversalTime().ToString('o')
    processes = $processes
}
$processState | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath (Join-Path $runtimeRoot 'processes.json') -Encoding UTF8

if (-not (Wait-HttpReady -Url 'http://127.0.0.1:8080/api/v1/health' -TimeoutSeconds 30)) {
    throw "后端启动超时，请查看 $logRoot\backend.error.log"
}
if (-not (Wait-HttpReady -Url "http://127.0.0.1:$WebPort" -TimeoutSeconds 30)) {
    throw "前端启动超时，请查看 $logRoot\frontend.error.log"
}

Write-Host ''
Write-Host 'cc-hardness 已启动：' -ForegroundColor Green
Write-Host "  Web:  http://127.0.0.1:$WebPort"
Write-Host '  API:  http://127.0.0.1:8080'
Write-Host "  日志: $logRoot"

if (-not $NoBrowser) {
    Start-Process "http://127.0.0.1:$WebPort"
}
} catch {
    foreach ($processId in $startedProcessIds) {
        Stop-ProcessTree -ProcessId $processId
    }
    throw
}
