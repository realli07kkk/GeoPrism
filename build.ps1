# GeoPrism 跨平台编译脚本 (PowerShell)

$ErrorActionPreference = "Stop"

$APP_NAME = "geoprism"
$OUTPUT_DIR = "bin"

# 获取版本信息
try {
    $VERSION = git describe --tags --always --dirty 2>$null
    if (-not $VERSION) { $VERSION = "dev" }
} catch {
    $VERSION = "dev"
}
$BUILD_TIME = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")

Write-Host "编译 $APP_NAME $VERSION"
Write-Host "================================"

if (Test-Path $OUTPUT_DIR) { Remove-Item -Recurse -Force $OUTPUT_DIR }
New-Item -ItemType Directory -Path $OUTPUT_DIR | Out-Null

# 目标平台列表
$targets = @(
    @{ GOOS="linux";   GOARCH="amd64"; Suffix="" },
    @{ GOOS="linux";   GOARCH="arm64"; Suffix="" },
    @{ GOOS="darwin";  GOARCH="arm64"; Suffix="" },
    @{ GOOS="darwin";  GOARCH="amd64"; Suffix="" },
    @{ GOOS="windows"; GOARCH="amd64"; Suffix=".exe" }
)

foreach ($t in $targets) {
    $output = "$OUTPUT_DIR/$APP_NAME-$($t.GOOS)-$($t.GOARCH)$($t.Suffix)"
    Write-Host "  -> $($t.GOOS)/$($t.GOARCH)"

    $env:CGO_ENABLED = "0"
    $env:GOOS = $t.GOOS
    $env:GOARCH = $t.GOARCH

    go build -trimpath `
        -ldflags="-s -w -X main.Version=$VERSION -X main.BuildTime=$BUILD_TIME" `
        -o $output .
}

# 恢复环境变量
Remove-Item Env:GOOS -ErrorAction SilentlyContinue
Remove-Item Env:GOARCH -ErrorAction SilentlyContinue

Write-Host "================================"
Write-Host "编译完成，产物在 $OUTPUT_DIR/ 目录："
Get-ChildItem $OUTPUT_DIR | Format-Table Name, Length
