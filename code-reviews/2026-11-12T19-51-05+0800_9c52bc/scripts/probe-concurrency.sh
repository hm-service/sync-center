#!/usr/bin/env bash
# 交叉审查探针：验证 /api/auth 与 /v1 共用 apiGate 造成的影响，以及 Argon2id 并发内存。
# 用法：scripts/probe-concurrency.sh [二进制路径]
set -uo pipefail

BIN=/tmp/sc-probe/app
if [ "$#" -ge 1 ]; then BIN=$1; fi
PORT=18182
PROBE_PORT=
if [ -n "$PROBE_PORT" ]; then PORT=$PROBE_PORT; fi
MAXAPI=8
ROOT=$(mktemp -d /tmp/sc-conc-probe.XXXXXX)
DATA=$ROOT/data
TOK=TOKENRW_0123456789
BASE=http://127.0.0.1:$PORT
SRV=

mkdir -p "$DATA"
cat > "$DATA/config.yaml" <<'YAML'
namespaces:
  - name: "default"
    tokens:
      - "TOKENRW_0123456789:rw"
YAML

cleanup() { if [ -n "$SRV" ]; then kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null; fi; }
trap cleanup EXIT

DATA_DIR="$DATA" PORT="$PORT" HOST=127.0.0.1 MAX_API_CONCURRENCY=$MAXAPI \
  AUTH_TOKEN=AUTHTOKEN_0123456789 SESSION_SIGNING_KEY=SIGNINGKEY_0123456789_0123456789 \
  "$BIN" > "$ROOT/server.log" 2>&1 &
SRV=$!
for _ in $(seq 100); do curl -sf -o /dev/null "$BASE/livez" && break; sleep 0.1; done

rss() { awk '/VmHWM/ {print $2}' /proc/$SRV/status; }
time_metadata() {
  curl -s -o /dev/null -w '%{time_total}' -H "Authorization: Bearer $TOK" "$BASE/v1/metadata"
}

echo "MAX_API_CONCURRENCY=$MAXAPI  pid=$SRV"
echo "空载 VmHWM(KB) = $(rss)"
echo "空载 /v1/metadata 耗时(s) = $(time_metadata)"

# 并发登录：都是错误口令，Argon2id 仍会完整计算，并且都要等满 1 秒才返回
PIDS=""
for i in $(seq 1 "$MAXAPI"); do
  curl -s -o /dev/null -X POST -d '{"token":"definitely-wrong-token"}' "$BASE/api/auth" &
  PIDS="$PIDS $!"
done
sleep 0.25
echo "登录风暴进行中 VmHWM(KB) = $(rss)"
echo "登录风暴进行中 /v1/metadata 耗时(s) = $(time_metadata)"
for p in $PIDS; do wait "$p"; done
echo "风暴结束后 VmHWM(KB) = $(rss)"
echo "风暴结束后 /v1/metadata 耗时(s) = $(time_metadata)"
echo
echo "### 第二阶段：只发请求头、不发请求体的慢连接（未认证）"
echo "用 $MAXAPI 条半开连接占住 /api/auth 的配额（Content-Length: 8192，但只发 1 字节）"
SLOW=""
for i in $(seq 1 "$MAXAPI"); do
  python3 - "$PORT" <<'PY' &
import socket, sys, time
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])))
s.sendall(b"POST /api/auth HTTP/1.1\r\nHost: probe\r\nContent-Length: 8192\r\nContent-Type: application/json\r\n\r\n{")
time.sleep(3.5)
s.close()
PY
  SLOW="$SLOW $!"
done
sleep 0.6
OUT=$(curl -s -o /dev/null -w '%{http_code} %{time_total}' --max-time 3 -H "Authorization: Bearer $TOK" "$BASE/v1/metadata")
RC=$?
echo "慢连接占满配额时 /v1/metadata => curl rc=$RC 输出='$OUT'（rc=28 表示 3 秒超时仍未拿到响应）"
for p in $SLOW; do wait "$p" 2>/dev/null; done
echo "慢连接全部关闭后 /v1/metadata 耗时(s) = $(time_metadata)"

echo
echo "说明：单次 Argon2id 申请 19 MiB（argonMemory = 19*1024 KiB），"
echo "MAX_API_CONCURRENCY 默认 128，因此登录并发内存上限约为 128 x 19 MiB。"
