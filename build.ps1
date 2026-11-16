<#
.SYNOPSIS
    构建 govdoc.exe 到项目根目录。

.DESCRIPTION
    依次完成两件事：
      1. 构建前端（web/dist）。它会被 go:embed 打进二进制，所以必须先构建，
         否则打出来的 exe 里是旧界面。
      2. 编译 Go 程序，输出到项目根目录的 govdoc.exe。

    任一步失败立即停止，不会留下一个"看起来构建成功"的半成品。

.PARAMETER SkipFrontend
    跳过前端构建，直接用现有的 web/dist。仅适用于只改了 Go 代码、且确认
    web/dist 已是当前版本的情况。正常构建不要加这个参数。

.PARAMETER Output
    输出文件名，默认是项目根目录的 govdoc.exe。

.EXAMPLE
    .\build.ps1

.EXAMPLE
    .\build.ps1 -SkipFrontend
#>
[CmdletBinding()]
param(
    [switch]$SkipFrontend,
    [string]$Output = 'govdoc.exe'
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

$root = $PSScriptRoot
$web = Join-Path $root 'web'

function Step {
    param([string]$Message)
    Write-Host ''
    Write-Host "==> $Message" -ForegroundColor Cyan
}

function Fail {
    param([string]$Message)
    Write-Host ''
    Write-Host "构建失败：$Message" -ForegroundColor Red
    exit 1
}

Push-Location $root
try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Fail '找不到 go 命令。请先安装 Go 并把它加入 PATH。'
    }

    if (-not $SkipFrontend) {
        if (-not (Get-Command pnpm -ErrorAction SilentlyContinue)) {
            Fail '找不到 pnpm 命令。请先安装 pnpm（或改用 -SkipFrontend，前提是 web/dist 已是最新）。'
        }
        if (-not (Test-Path -LiteralPath (Join-Path $web 'node_modules'))) {
            Step '安装前端依赖（首次构建需要联网）'
            Push-Location $web
            try {
                pnpm install
                if ($LASTEXITCODE -ne 0) { Fail 'pnpm install 失败。' }
            }
            finally {
                Pop-Location
            }
        }

        Step '构建前端（web/dist，会被内嵌进 exe）'
        Push-Location $web
        try {
            pnpm run build
            if ($LASTEXITCODE -ne 0) { Fail '前端构建失败，exe 未生成。' }
        }
        finally {
            Pop-Location
        }
    }
    else {
        Write-Host '已跳过前端构建，使用现有的 web/dist。' -ForegroundColor Yellow
        if (-not (Test-Path -LiteralPath (Join-Path $web 'dist\index.html'))) {
            Fail 'web/dist 不存在或为空，无法内嵌界面。请去掉 -SkipFrontend 重新构建。'
        }
    }

    Step "编译 govdoc.exe"
    # -trimpath 去掉本机绝对路径，产物更干净、也更接近发布版。
    # CGO 关掉，保证产物不依赖本机 C 运行库。
    $env:CGO_ENABLED = '0'
    go build -trimpath -o $Output ./cmd/govdoc
    if ($LASTEXITCODE -ne 0) { Fail 'go build 失败。' }

    $exe = Join-Path $root $Output
    if (-not (Test-Path -LiteralPath $exe)) {
        Fail "编译命令返回成功，但没有找到产物：$exe"
    }
    $size = [math]::Round((Get-Item -LiteralPath $exe).Length / 1MB, 1)
    Write-Host ''
    Write-Host "构建完成：$exe（$size MB）" -ForegroundColor Green
}
finally {
    Pop-Location
}
