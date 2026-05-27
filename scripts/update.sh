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

DIR=$(find_compose_dir 2>/dev/null || true)

upgrade_via_compose() {
  echo "📂 docker-compose 部署目录: $1"
  cd "$1"
  local PORT_GUESS
  PORT_GUESS=$(grep -oE 'localhost:[0-9]+/api/version' "$1/docker-compose.yml" 2>/dev/null | head -1 \
                | grep -oE '[0-9]+' || echo "3001")
  CURRENT=$(curl -fs -m 2 "http://localhost:${PORT_GUESS}/api/version" 2>/dev/null \
    | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
  [[ -n "$CURRENT" ]] && echo "📌 当前版本: $CURRENT"
  echo "📦 拉取最新镜像..."
  docker compose pull
  echo "🚀 重启服务..."
  docker compose up -d
  sleep 5
  NEW=$(curl -fs -m 2 "http://localhost:${PORT_GUESS}/api/version" 2>/dev/null \
    | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
  [[ -n "$NEW" ]] && echo "📌 新版本:   $NEW"
  echo "✅ 升级完成!"
}

upgrade_via_docker_run() {
  # docker run 部署: 通过 docker inspect 重组原始 args, pull 新 image 后重启
  local container="$1"
  echo "🐳 docker-run 部署 (无 compose), 容器名: $container"

  # 取容器原始配置
  local image port_map env_args volume_args restart name
  image=$(docker inspect -f '{{.Config.Image}}' "$container")
  restart=$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$container")
  name="$container"

  # ports
  port_map=$(docker inspect -f '{{range $p, $conf := .NetworkSettings.Ports}}{{range $conf}}-p {{.HostPort}}:{{$p}} {{end}}{{end}}' "$container" \
    | sed -E 's|/tcp||g; s|/udp||g' | tr -s ' ')

  # env (skip docker default PATH)
  env_args=$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$container" \
    | grep -v "^PATH=" | grep -v "^$" | awk '{printf "-e %s ", $0}')

  # volumes
  volume_args=$(docker inspect -f '{{range .Mounts}}-v {{.Source}}:{{.Destination}} {{end}}' "$container")

  # 探测当前 host port (取 first mapped port)
  local HOST_PORT
  HOST_PORT=$(docker inspect -f '{{range $p, $conf := .NetworkSettings.Ports}}{{range $conf}}{{.HostPort}} {{end}}{{end}}' "$container" \
    | tr ' ' '\n' | grep -v '^$' | head -1)
  HOST_PORT=${HOST_PORT:-3001}

  CURRENT=$(curl -fs -m 2 "http://localhost:${HOST_PORT}/api/version" 2>/dev/null \
    | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
  [[ -n "$CURRENT" ]] && echo "📌 当前版本: $CURRENT"

  echo "📦 拉取最新镜像: $image"
  docker pull "$image"

  echo "🛑 停掉旧容器..."
  docker stop "$container" >/dev/null
  docker rm "$container" >/dev/null

  echo "🚀 用同样参数启动新容器..."
  echo "   docker run -d --name $name --restart $restart $port_map $env_args $volume_args $image"
  eval "docker run -d --name $name --restart $restart $port_map $env_args $volume_args $image"

  sleep 5
  NEW=$(curl -fs -m 2 "http://localhost:${HOST_PORT}/api/version" 2>/dev/null \
    | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
  [[ -n "$NEW" ]] && echo "📌 新版本:   $NEW"
  echo "✅ 升级完成!"
}

if [[ -z "$DIR" ]]; then
  # 尝试 docker run fallback
  if command -v docker > /dev/null 2>&1; then
    AUTO_CONTAINER=$(docker ps --format '{{.Names}} {{.Image}}' 2>/dev/null \
      | grep -E "autogateway|gpt-load" | awk '{print $1}' | head -1)
    if [[ -n "$AUTO_CONTAINER" ]]; then
      upgrade_via_docker_run "$AUTO_CONTAINER"
      exit 0
    fi
  fi
  echo "❌ 没找到 autogateway 部署 (既无 compose 也无 docker 容器)" >&2
  echo "" >&2
  echo "请用以下方式之一:" >&2
  echo "  1. cd 到 docker-compose.yml 目录再跑" >&2
  echo "  2. 显式 ENV:  AUTOGATEWAY_DIR=/your/path bash <(curl ...)" >&2
  echo "  3. 手动:      docker compose pull && docker compose up -d" >&2
  exit 1
fi

upgrade_via_compose "$DIR"
exit 0

echo "⏳ 等服务起来..."
sleep 5

NEW=$(curl -fs -m 2 http://localhost:3001/api/version 2>/dev/null \
  | grep -oE '"version":"v[^"]+"' | head -1 | sed -E 's/.*"(v[^"]+)"/\1/')
if [[ -n "$NEW" ]]; then
  echo "📌 新版本:   $NEW"
fi
echo "✅ 升级完成!"
