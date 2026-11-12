#!/usr/bin/env bash
# 交叉审查探针：同一 namespace 里重复配置同一个 token 时，权限被静默覆盖。
# 用法：scripts/probe-config.sh [二进制路径]
set -uo pipefail

BIN=/tmp/sc-probe/app
if [ "$#" -ge 1 ]; then BIN=$1; fi
ROOT=$(mktemp -d /tmp/sc-config-probe.XXXXXX)
TOK=TOKENRW_0123456789
SRV=

cleanup() { if [ -n "$SRV" ]; then kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null; fi; }
trap cleanup EXIT

run_case() { # $1=端口 $2=第一行权限 $3=第二行权限
  local port=$1 first=$2 second=$3 dir=$ROOT/case-$1
  mkdir -p "$dir"
  cat > "$dir/config.yaml" <<YAML
namespaces:
  - name: "default"
    tokens:
      - "$TOK:$first"
      - "$TOK:$second"
YAML
  DATA_DIR="$dir" PORT="$port" HOST=127.0.0.1 \
    AUTH_TOKEN=AUTHTOKEN_0123456789 SESSION_SIGNING_KEY=SIGNINGKEY_0123456789_0123456789 \
    "$BIN" > "$dir/server.log" 2>&1 &
  SRV=$!
  local ok=no
  for _ in $(seq 100); do curl -sf -o /dev/null "http://127.0.0.1:$port/livez" && ok=yes && break; sleep 0.1; done
  echo "配置顺序 [$first, $second]：启动 $ok"
  if [ "$ok" = yes ]; then
    echo "  /v1/metadata => $(curl -s -H "Authorization: Bearer $TOK" "http://127.0.0.1:$port/v1/metadata" | head -c 120)"
    echo "  POST /v1/lease => $(curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $TOK" "http://127.0.0.1:$port/v1/lease")"
  else
    tail -1 "$dir/server.log"
  fi
  kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null; SRV=
}

echo "同一 token 在同一个 namespace 里出现两次，且权限不同："
run_case 18183 rw r
run_case 18184 r rw
