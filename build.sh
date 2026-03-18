#!/usr/bin/env bash
# GeoPrism 跨平台编译脚本

set -e

APP_NAME="geoprism"
OUTPUT_DIR="bin"
MODULE=$(go list -m)

# 获取版本信息（优先使用 git tag，否则用 commit hash）
VERSION=$(git describe --tags --always --dirty 2>/dev/null || echo "dev")
BUILD_TIME=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

echo "编译 ${APP_NAME} ${VERSION}"
echo "================================"

rm -rf "${OUTPUT_DIR}"
mkdir -p "${OUTPUT_DIR}"

# 目标平台列表: GOOS/GOARCH/后缀
TARGETS=(
  "linux/amd64/"
  "linux/arm64/"
  "darwin/arm64/"
  "darwin/amd64/"
  "windows/amd64/.exe"
)

for target in "${TARGETS[@]}"; do
  IFS='/' read -r os arch suffix <<< "${target}"
  output="${OUTPUT_DIR}/${APP_NAME}-${os}-${arch}${suffix}"

  echo "  -> ${os}/${arch}"
  CGO_ENABLED=0 GOOS="${os}" GOARCH="${arch}" \
    go build -trimpath \
      -ldflags="-s -w -X main.Version=${VERSION} -X main.BuildTime=${BUILD_TIME}" \
      -o "${output}" .
done

echo "================================"
echo "编译完成，产物在 ${OUTPUT_DIR}/ 目录："
ls -lh "${OUTPUT_DIR}/"
