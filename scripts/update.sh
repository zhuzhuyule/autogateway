#!/bin/bash
# AutoGateway 一键升级脚本
#
# 用法:
#   bash <(curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/update.sh)
#   AUTOGATEWAY_DIR=/opt/autogateway bash <(curl ...)
#
# 自动查找顺序:
#   1. 环境变量 AUTOGATEWAY_DIR
#   2. 当前目录的 docker-compose.yml
#   3. docker ps 找 autogateway 容器 → 通过 com.docker.compose.project.working_dir 标签
#   4. 常见路径: /opt/autogateway /srv/autogateway ~/autogateway 等
#   都找不到 → 提示用户手动指定

set -euo pipefail

find_compose_dir() {
  # 1. ENV
  if [[ -n "${AUTOGATEWAY_DIR:-}" ]] && [[ -f "$AUTOGATEWAY_DIR/docker-compose.yml" ]]; then
    echo "$AUTOGATEWAY_DIR"; return 0
  fi

  # 2. 当前目录 (仅当 docker-compose.yml 引用了 autogateway/gpt-load 镜像)
  if [[ -f "$(pwd)/docker-compose.yml" ]] && grep -qE "autogateway|gpt-load" "$(pwd)/docker-compose.yml" 2>/dev/null; then
    echo "$(pwd)"; return 0
  fi

  # 3. docker ps 自动找
  if command -v docker > /dev/null 2>&1; then
    local container
    container=$(docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null \
      | grep -E "autogateway|gpt-load" | awk '{print $1}' | head -1)
    if [[ -n "$container" ]]; then
      local workdir
      workdir=$(docker inspect -f '{{ index .Config.Labels "com.docker.compose.project.working_dir" }}' "$container" 2>/dev/null || echo "")
      if [[ -n "$workdir" ]] && [[ -f "$workdir/docker-compose.yml" ]]; then
        echo "$workdir"; return 0
      fi
    fi
  fi

  # 4. 常见路径
  for p in /opt/autogateway /srv/autogateway "$HOME/autogateway" /opt/gpt-load /srv/gpt-load "$HOME/gpt-load"; do
    if [[ -f "$p/docker-compose.yml" ]]; then
      echo "$p"; return 0
    fi
  done

  return 1
}

DIR=$(find_compose_dir 2>/dev/null) || {
  echo "❌ 没找到 autogateway 的 docker-compose.yml" >&2
  echo "" >&2
  echo "请用以下方式之一:" >&2
  echo "  1. cd 到你部署目录再跑 (该目录有 docker-compose.yml)" >&2
  echo "  2. 显式指定 ENV: AUTOGATEWAY_DIR=/your/path bash <(curl ...)" >&2
  echo "  3. 手动:        cd <部署目录> && docker compose pull && docker compose up -d" >&2
  exit 1
}

echo "📂 部署目录: $DIR"
cd "$DIR"

CURRENT=$(curl -fs -m 2 http://localhost:3001/api/version 2>/dev/null \
  | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
[[ -n "$CURRENT" ]] && echo "📌 当前版本: $CURRENT"

echo "📦 拉取最新镜像..."
docker compose pull

echo "🚀 重启服务..."
docker compose up -d

echo "⏳ 等服务起来..."
sleep 5

NEW=$(curl -fs -m 2 http://localhost:3001/api/version 2>/dev/null \
  | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
if [[ -n "$NEW" ]]; then
  echo "📌 新版本:   $NEW"
fi
echo "✅ 升级完成!"
