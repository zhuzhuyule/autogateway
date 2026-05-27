#!/bin/bash
# AutoGateway 一键升级脚本
# 用法: cd <部署目录> && bash <(curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/update.sh)
#   或: AUTOGATEWAY_DIR=/opt/autogateway bash <(curl ...)

set -euo pipefail

DIR="${AUTOGATEWAY_DIR:-$(pwd)}"
cd "$DIR"

if [[ ! -f docker-compose.yml ]]; then
  echo "❌ docker-compose.yml 未找到 (当前目录: $DIR)"
  echo "   请在 docker-compose.yml 所在目录运行, 或指定 AUTOGATEWAY_DIR=<path>"
  exit 1
fi

echo "📦 拉取最新镜像..."
docker compose pull
echo "🚀 重启服务..."
docker compose up -d
echo ""
echo "✅ 升级完成! 看版本:  curl -s http://localhost:3001/api/version | jq"
