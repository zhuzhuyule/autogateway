#!/usr/bin/env bash
# AutoGateway in-place upgrade.
#
# 不用记任何参数 — 从现有容器 inspect 出 env / port / volume / restart policy,
# 拉新镜像, 替换容器, 数据卷保留. 镜像没变化时立刻退出, 不重启。
#
# === Usage ===
#
#   curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/update.sh | bash
#
# Optional overrides:
#   CONTAINER_NAME=autogateway    target container (default: autogateway)
#   IMAGE_TAG=latest              tag to upgrade to (default: latest; can pin e.g. 1.1.2)
#   FORCE=1                       recreate even when digest unchanged
#
# 该脚本不会创建新容器 — 只升级已存在的那个. 首次部署请用 install.sh.

set -euo pipefail

say()  { printf "\033[1;36m[autogateway]\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m[autogateway]\033[0m %s\n" "$*" >&2; }
die()  { printf "\033[1;31m[autogateway]\033[0m %s\n" "$*" >&2; exit 1; }

CONTAINER_NAME="${CONTAINER_NAME:-autogateway}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
FORCE="${FORCE:-0}"
NEW_IMAGE="ghcr.io/zhuzhuyule/autogateway:${IMAGE_TAG}"

# ----- 0. precond -----
command -v docker >/dev/null 2>&1 || die "docker not found"
docker info >/dev/null 2>&1 || die "docker daemon not reachable"

if ! docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  die "container '$CONTAINER_NAME' does not exist. Run install.sh first."
fi

# ----- 1. record current state -----
OLD_DIGEST=$(docker container inspect --format='{{.Image}}' "$CONTAINER_NAME")
OLD_IMAGE_REF=$(docker container inspect --format='{{.Config.Image}}' "$CONTAINER_NAME")
say "Current: $OLD_IMAGE_REF (digest ${OLD_DIGEST:7:12})"

# ----- 2. pull new image -----
say "Pulling $NEW_IMAGE ..."
docker pull "$NEW_IMAGE" >/dev/null
NEW_DIGEST=$(docker image inspect --format='{{.Id}}' "$NEW_IMAGE")

if [ "$OLD_DIGEST" = "$NEW_DIGEST" ] && [ "$FORCE" != "1" ]; then
  say "Already at latest (${NEW_DIGEST:7:12}). Nothing to do. Use FORCE=1 to recreate."
  exit 0
fi

say "New: $NEW_IMAGE (digest ${NEW_DIGEST:7:12})"

# ----- 3. extract run-time config from current container -----
say "Reading config from running container..."

ENV_LINES=$(docker container inspect "$CONTAINER_NAME" --format='{{range .Config.Env}}{{println .}}{{end}}')
RESTART=$(docker container inspect "$CONTAINER_NAME" --format='{{.HostConfig.RestartPolicy.Name}}')
[ -z "$RESTART" ] && RESTART="unless-stopped"

# Port bindings: take HostConfig.PortBindings (用户显式 -p 的; 跳过 docker
# 自动追加的 IPv6 镜像绑定, 避免 [::]:port 解析歧义).
# 输出格式: "container_port_with_proto|host_ip|host_port" 每行一条.
PORT_ARGS=""
while IFS='|' read -r cport hostip hostport; do
  cport=$(echo "$cport" | tr -d ' ')
  hostip=$(echo "$hostip" | tr -d ' ')
  hostport=$(echo "$hostport" | tr -d ' ')
  [ -z "$cport" ] || [ -z "$hostport" ] && continue
  if [ -z "$hostip" ] || [ "$hostip" = "0.0.0.0" ]; then
    PORT_ARGS="$PORT_ARGS -p ${hostport}:${cport}"
  else
    PORT_ARGS="$PORT_ARGS -p ${hostip}:${hostport}:${cport}"
  fi
done < <(docker container inspect "$CONTAINER_NAME" --format='{{range $p, $b := .HostConfig.PortBindings}}{{range $b}}{{println $p "|" .HostIp "|" .HostPort}}{{end}}{{end}}')

# Volume bindings (named volumes or bind mounts)
VOL_ARGS=$(docker container inspect "$CONTAINER_NAME" --format='{{range .Mounts}}{{if eq .Type "volume"}}-v {{.Name}}:{{.Destination}} {{else if eq .Type "bind"}}-v {{.Source}}:{{.Destination}} {{end}}{{end}}')

# Env vars (skip PATH and other docker-injected ones — preserve only user-set)
ENV_ARGS=""
while IFS= read -r kv; do
  [ -z "$kv" ] && continue
  case "$kv" in
    PATH=*|HOME=*|HOSTNAME=*) ;;
    *) ENV_ARGS="$ENV_ARGS -e $kv" ;;
  esac
done <<< "$ENV_LINES"

# ----- 4. stop & remove old container -----
say "Stopping & removing old container..."
docker stop "$CONTAINER_NAME" >/dev/null
docker rm   "$CONTAINER_NAME" >/dev/null

# ----- 5. run new container with same config -----
say "Starting new container..."
# shellcheck disable=SC2086
docker run -d \
  --name "$CONTAINER_NAME" \
  --restart "$RESTART" \
  $PORT_ARGS \
  $VOL_ARGS \
  $ENV_ARGS \
  "$NEW_IMAGE" >/dev/null

# ----- 6. wait for /health -----
# 不依赖 docker 自带的 healthcheck (老镜像/手动 docker run 都可能没配),
# 直接打 /health 端点更可靠.
HOST_PORT=$(docker container inspect "$CONTAINER_NAME" --format='{{range $p, $b := .HostConfig.PortBindings}}{{range $b}}{{.HostPort}} {{end}}{{end}}' | awk '{print $1}')
[ -z "$HOST_PORT" ] && HOST_PORT=3001
say "Waiting for http://localhost:${HOST_PORT}/health ..."
ok=0
for i in $(seq 1 30); do
  if curl -fsS -m 2 "http://localhost:${HOST_PORT}/health" >/dev/null 2>&1; then
    ok=1
    break
  fi
  sleep 2
done

if [ "$ok" != "1" ]; then
  warn "Service did not respond on /health in 60s. Logs:"
  docker logs --tail=30 "$CONTAINER_NAME"
  exit 1
fi

# ----- 7. summary -----
printf '\n\033[1;32m✓ Upgraded successfully.\033[0m\n\n'
cat <<EOF
  URL:        http://localhost:${HOST_PORT}
  Image:      ${NEW_IMAGE} (digest ${NEW_DIGEST:7:12})
  Was:        ${OLD_IMAGE_REF} (digest ${OLD_DIGEST:7:12})
  Container:  ${CONTAINER_NAME}

EOF
