#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${1:-$ROOT_DIR/.env}"
COMPOSE_FILE="${2:-$ROOT_DIR/docker-compose.yml}"
LOG_DIR="${ARKAPI_ADMIN_LOG_DIR:-/var/log/apache2}"
LOG_FILE="${ARKAPI_ADMIN_TRAFFIC_LOG_PATH_VALUE:-$LOG_DIR/arkapi-access.log}"
LOG_MOUNT="      - ${LOG_DIR}:${LOG_DIR}:ro"

if [[ ! -f "$ENV_FILE" ]]; then
  echo "env file not found: $ENV_FILE" >&2
  exit 1
fi

if [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "compose file not found: $COMPOSE_FILE" >&2
  exit 1
fi

tmp_env="$(mktemp)"
tmp_compose="$(mktemp)"
cleanup() {
  rm -f "$tmp_env" "$tmp_compose"
}
trap cleanup EXIT

changed=0

if grep -q '^ARKAPI_ADMIN_TRAFFIC_LOG_PATH=' "$ENV_FILE"; then
  sed "s#^ARKAPI_ADMIN_TRAFFIC_LOG_PATH=.*#ARKAPI_ADMIN_TRAFFIC_LOG_PATH=${LOG_FILE}#" "$ENV_FILE" >"$tmp_env"
else
  cat "$ENV_FILE" >"$tmp_env"
  printf '\nARKAPI_ADMIN_TRAFFIC_LOG_PATH=%s\n' "$LOG_FILE" >>"$tmp_env"
fi

if ! cmp -s "$ENV_FILE" "$tmp_env"; then
  cp "$tmp_env" "$ENV_FILE"
  changed=1
fi

if ! grep -Fq "$LOG_MOUNT" "$COMPOSE_FILE"; then
  awk -v mount_line="$LOG_MOUNT" '
    BEGIN { in_arkapi = 0; inserted = 0 }
    /^  arkapi:$/ { in_arkapi = 1 }
    in_arkapi && /^  [^ ]/ && $0 != "  arkapi:" {
      if (!inserted) {
        print mount_line
        inserted = 1
      }
      in_arkapi = 0
    }
    { print }
    in_arkapi && $0 == "      - ./geoip:/geoip:ro" {
      print mount_line
      inserted = 1
    }
    END {
      if (!inserted) {
        exit 2
      }
    }
  ' "$COMPOSE_FILE" >"$tmp_compose"
  cp "$tmp_compose" "$COMPOSE_FILE"
  changed=1
fi

if [[ "$changed" -eq 1 ]]; then
  echo "updated admin traffic configuration"
else
  echo "admin traffic configuration already up to date"
fi

echo "env: $ENV_FILE"
echo "compose: $COMPOSE_FILE"
echo "log path: $LOG_FILE"
echo "mount: $LOG_MOUNT"
