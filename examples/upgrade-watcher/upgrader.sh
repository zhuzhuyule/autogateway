#!/bin/bash
# AutoGateway upgrade watcher
#
# 监听容器写入的 .upgrade-request 信号文件, 触发 docker compose 升级.
# 主容器零特权 — 真正调用 docker daemon 的权限只在本脚本里.
#
# 部署: 把本脚本放在 /opt/autogateway/upgrader.sh,
#       配合 systemd service autogateway-upgrader.service 运行.
#
# 环境变量:
#   AUTOGATEWAY_DIR     - docker compose 目录, 默认 /opt/autogateway
#   AUTOGATEWAY_DATA    - 容器挂载的 data 目录, 默认 $AUTOGATEWAY_DIR/data
#   POLL_INTERVAL       - 轮询间隔秒, 默认 10

set -euo pipefail

AUTOGATEWAY_DIR="${AUTOGATEWAY_DIR:-/opt/autogateway}"
AUTOGATEWAY_DATA="${AUTOGATEWAY_DATA:-$AUTOGATEWAY_DIR/data}"
SIGNAL="$AUTOGATEWAY_DATA/.upgrade-request"
LOG="$AUTOGATEWAY_DATA/.upgrade-log"
POLL_INTERVAL="${POLL_INTERVAL:-10}"

log() {
  local msg="$1"
  echo "$(date -Iseconds) $msg" | tee -a "$LOG" >&2
}

if [[ ! -d "$AUTOGATEWAY_DIR" ]]; then
  log "FATAL: AUTOGATEWAY_DIR=$AUTOGATEWAY_DIR does not exist"
  exit 1
fi

log "watcher started, polling $SIGNAL every ${POLL_INTERVAL}s"

while true; do
  if [[ -f "$SIGNAL" ]]; then
    # 简单 jq 不强制 - 用 grep 也能扛
    target=$(grep -oE '"target_version"\s*:\s*"[^"]+"' "$SIGNAL" | sed -E 's/.*"([^"]+)"$/\1/')
    by=$(grep -oE '"requested_by"\s*:\s*"[^"]+"' "$SIGNAL" | sed -E 's/.*"([^"]+)"$/\1/' || echo "unknown")
    log "signal detected: target=$target by=$by"

    # 删除信号文件 (无论成败, 防止反复触发)
    rm -f "$SIGNAL"

    cd "$AUTOGATEWAY_DIR"
    if docker compose pull && docker compose up -d; then
      log "upgrade succeeded: target=$target by=$by"
    else
      log "upgrade FAILED: target=$target by=$by"
    fi
  fi
  sleep "$POLL_INTERVAL"
done
