#!/usr/bin/env bash
# 启动一个独立实例（全新 DATA_DIR、非 18080 端口），依次跑全部探针，最后关掉实例。
# 用法：bash scripts/run-all.sh [PORT]
set -uo pipefail

PORT=${1:-18096}
REPO_ROOT=$(cd "$(dirname "$0")/../../.." && pwd)
APP=${APP:-/tmp/sc-review-c7f31d/app}
DATA=${DATA_DIR:-$(mktemp -d /tmp/sc-review-c7f31d-data.XXXXXX)}
CONFIG=${CONFIG_FILE:-/tmp/sync-test/config.yaml}
BASE="http://127.0.0.1:$PORT"

echo "repo     = $REPO_ROOT"
echo "binary   = $APP"
echo "data_dir = $DATA"
echo "base     = $BASE"

mkdir -p "$(dirname "$APP")"
export APP
if [ ! -x "$APP" ]; then
  ( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o "$APP" . ) || exit 1
fi

mkdir -p "$DATA"
AUTH_TOKEN=sync_center_panel \
SESSION_SIGNING_KEY=session_signing_key_7f3a9c2e5b8d1f4a6c0e9b3d7a2f5c8e \
GC_TOKEN=gc_token_2026_ops \
HOST=127.0.0.1 PORT="$PORT" DATA_DIR="$DATA" CONFIG_FILE="$CONFIG" \
  "$APP" > "$DATA/server.log" 2>&1 &
SERVER_PID=$!
cleanup() { kill "$SERVER_PID" 2>/dev/null; wait "$SERVER_PID" 2>/dev/null; }
trap cleanup EXIT

for _ in $(seq 40); do
  if curl -s -o /dev/null --max-time 1 "$BASE/livez"; then break; fi
  sleep 0.1
done
curl -s -o /dev/null -w 'livez=%{http_code}\n' "$BASE/livez"

here=$(cd "$(dirname "$0")" && pwd)
bash "$here/probe-login.sh" "$BASE"
bash "$here/probe-flock-gap.sh" "$BASE" "$DATA"
bash "$here/probe-guard-stall.sh" "$BASE" "$DATA"
# damaged-gc 放最后：它会在 work 留下损坏文件，之后该目录的回收会一直失败
bash "$here/probe-damaged-gc.sh" "$BASE" "$DATA"
echo "server log tail:"
tail -5 "$DATA/server.log"
