#!/bin/bash
# 构建 Linux amd64 最小化二进制(含内嵌管理界面)
#
# 用法: ./build.sh
# 输出: build/icloud-hme

set -e

OUTPUT_DIR="build"
BINARY_NAME="icloud-hme"

echo "==> 安装前端依赖"
npm --prefix web ci

echo "==> 检查并构建前端(lint + test + build)"
npm --prefix web run check

echo "==> 运行 Go 测试、竞态检测与静态检查"
go test ./...
go test -race ./...
go vet ./...

echo "==> 清理旧的构建文件"
rm -rf "$OUTPUT_DIR"
mkdir -p "$OUTPUT_DIR"

echo "==> 构建 Linux amd64 最小化二进制"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath \
    -ldflags="-s -w -buildid=" \
    -gcflags="-l=4" \
    -o "$OUTPUT_DIR/$BINARY_NAME" \
    .

echo "==> 压缩二进制(upx)"
if command -v upx >/dev/null 2>&1; then
  upx --best --lzma "$OUTPUT_DIR/$BINARY_NAME" || true
else
  echo "    (upx 未安装,跳过压缩)"
fi

echo ""
echo "==> 构建完成"
echo "    文件: $OUTPUT_DIR/$BINARY_NAME"
ls -lh "$OUTPUT_DIR/$BINARY_NAME"
