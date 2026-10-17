#!/usr/bin/env bash
# iCloud HME 启动脚本: 加载 .env 后启动服务。
# 用法: ./run.sh             (默认 :8081, 数据目录 ./data, 调试模式)
#       ./run.sh -addr :9000 (额外 flag 透传给二进制)
set -euo pipefail

cd "$(dirname "$0")"

if [[ ! -f .env ]]; then
  echo "缺少 .env 文件(需包含 ICLOUD_HME_ADMIN_PASSWORD)" >&2
  exit 1
fi

# 加载 .env: set -a 让所有赋值自动 export
set -a
# shellcheck disable=SC1091
. ./.env
set +a

BIN="${ICLOUD_HME_BIN:-./build/icloud-hme}"
if [[ ! -x "$BIN" ]]; then
  echo "二进制不存在或不可执行: $BIN (先执行: go build -o build/icloud-hme .)" >&2
  exit 1
fi

# 命令行 flag 默认值; 允许调用者覆盖
ADDR="${ICLOUD_HME_ADDR:-:8081}"
DATA="${ICLOUD_HME_DATA:-./data}"
DEBUG_FLAG=(-debug)

echo "启动: $BIN -addr $ADDR -data $DATA ${DEBUG_FLAG[*]}"
exec "$BIN" -addr "$ADDR" -data "$DATA" "${DEBUG_FLAG[@]}" "$@"
