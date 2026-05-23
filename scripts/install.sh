#!/usr/bin/env bash
# AutoGateway one-line installer.
#
# === Quickest start (defaults: auto-generate AUTH_KEY, port 3001) ===
#
#   curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/install.sh | bash
#
# === Common: pick your own host port + login key ===
#
#   curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/install.sh | \
#     HOST_PORT=8080 AUTH_KEY=sk-mypassword bash
#
# === Full control via CLI flags (saves to local install.sh first) ===
#
#   curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/install.sh -o install.sh
#   chmod +x install.sh
#   ./install.sh \
#     --port 8080 \
#     --auth-key sk-mypassword \
#     --encryption-key my-32-char-secret \
#     --image-tag 1.1.0
#
# === All supported overrides (env var = CLI flag) ===
#
#   HOST_PORT         --port            Host port mapped to container's 3001. Default: 3001
#   AUTH_KEY          --auth-key        Admin login key. Default: auto-generated sk-<32 random>
#   ENCRYPTION_KEY    --encryption-key  DB field encryption. Default: "" (dev mode noop)
#   IMAGE_TAG         --image-tag       latest | 1.1 | 1.1.0 etc. Default: latest
#   CONTAINER_NAME    --name            Default: autogateway
#   DATA_VOLUME       --data-volume     Named volume for /app/data. Default: autogateway-data
#   SKIP_DOCKER_INSTALL=1               Do NOT auto-install docker if missing.
#
# Re-running the script upgrades in place: pulls the new image, recreates the
# container, keeps the data volume intact.

set -euo pipefail

# ----- pretty -----
say()  { printf "\033[1;36m[autogateway]\033[0m %s\n" "$*"; }
warn() { printf "\033[1;33m[autogateway]\033[0m %s\n" "$*" >&2; }
die()  { printf "\033[1;31m[autogateway]\033[0m %s\n" "$*" >&2; exit 1; }

# ----- defaults (env vars win over hardcoded; CLI flags win over env vars) -----
HOST_PORT="${HOST_PORT:-${PORT:-3001}}"          # PORT kept for backward-compat
CONTAINER_PORT=3001                              # fixed inside the image
AUTH_KEY="${AUTH_KEY:-}"
ENCRYPTION_KEY="${ENCRYPTION_KEY:-}"
IMAGE_TAG="${IMAGE_TAG:-latest}"
CONTAINER_NAME="${CONTAINER_NAME:-autogateway}"
DATA_VOLUME="${DATA_VOLUME:-autogateway-data}"

# ----- parse CLI flags (override env vars) -----
while [ $# -gt 0 ]; do
  case "$1" in
    --port)            HOST_PORT="$2";       shift 2 ;;
    --auth-key)        AUTH_KEY="$2";        shift 2 ;;
    --encryption-key)  ENCRYPTION_KEY="$2";  shift 2 ;;
    --image-tag)       IMAGE_TAG="$2";       shift 2 ;;
    --name)            CONTAINER_NAME="$2";  shift 2 ;;
    --data-volume)     DATA_VOLUME="$2";     shift 2 ;;
    -h|--help)
      sed -n '2,/^set -e/p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) die "Unknown flag: $1 (try --help)" ;;
  esac
done

IMAGE="ghcr.io/zhuzhuyule/autogateway:${IMAGE_TAG}"

# ----- 1. ensure docker -----
if ! command -v docker >/dev/null 2>&1; then
  if [ "${SKIP_DOCKER_INSTALL:-0}" = "1" ]; then
    die "docker not found. Install it manually then re-run, or unset SKIP_DOCKER_INSTALL."
  fi
  case "$(uname -s)" in
    Linux)
      say "Installing Docker via get.docker.com (sudo required)..."
      curl -fsSL https://get.docker.com | sh
      sudo systemctl enable --now docker 2>/dev/null || true
      ;;
    Darwin)
      die "Docker not detected on macOS. Install Docker Desktop or OrbStack first."
      ;;
    *)
      die "Unsupported OS: $(uname -s). Install Docker manually."
      ;;
  esac
fi

if ! docker info >/dev/null 2>&1; then
  die "docker daemon not reachable. Start it (e.g. 'sudo systemctl start docker') and retry."
fi

# ----- 2. generate AUTH_KEY if unset -----
if [ -z "$AUTH_KEY" ]; then
  AUTH_KEY="sk-$(LC_ALL=C tr -dc 'a-zA-Z0-9' </dev/urandom | head -c 32)"
  say "AUTH_KEY auto-generated. Save it now: $AUTH_KEY"
fi

# ----- 3. pull image -----
say "Pulling $IMAGE ..."
docker pull "$IMAGE"

# ----- 4. ensure data volume -----
docker volume inspect "$DATA_VOLUME" >/dev/null 2>&1 || docker volume create "$DATA_VOLUME" >/dev/null

# ----- 5. stop & remove old container if any -----
if docker container inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
  say "Existing container '$CONTAINER_NAME' found — stopping for upgrade..."
  docker stop "$CONTAINER_NAME" >/dev/null
  docker rm   "$CONTAINER_NAME" >/dev/null
fi

# ----- 6. run -----
say "Starting '$CONTAINER_NAME' on host port $HOST_PORT (→ container ${CONTAINER_PORT})..."
ENV_ARGS=(-e "AUTH_KEY=$AUTH_KEY" -e "PORT=${CONTAINER_PORT}")
[ -n "$ENCRYPTION_KEY" ] && ENV_ARGS+=(-e "ENCRYPTION_KEY=$ENCRYPTION_KEY")

docker run -d \
  --name "$CONTAINER_NAME" \
  --restart unless-stopped \
  -p "${HOST_PORT}:${CONTAINER_PORT}" \
  -v "${DATA_VOLUME}:/app/data" \
  "${ENV_ARGS[@]}" \
  "$IMAGE" >/dev/null

# ----- 7. wait for /health -----
# 不依赖镜像自带的 docker healthcheck (docker run 不会从 compose.yml 继承),
# 直接打 /health 端点更可靠.
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

# ----- 8. summary -----
printf '\n\033[1;32m✓ AutoGateway is running.\033[0m\n\n'
cat <<EOF
  URL:        http://localhost:${HOST_PORT}
  Auth key:   ${AUTH_KEY}
  Container:  ${CONTAINER_NAME}  (data volume: ${DATA_VOLUME})
  Image:      ${IMAGE}

Upgrade later (same command — data preserved):
  curl -fsSL https://raw.githubusercontent.com/zhuzhuyule/autogateway/main/scripts/install.sh | \\
    HOST_PORT=${HOST_PORT} AUTH_KEY=${AUTH_KEY} bash

Stop / remove:
  docker stop ${CONTAINER_NAME} && docker rm ${CONTAINER_NAME}
  docker volume rm ${DATA_VOLUME}        # also wipes the database

EOF
