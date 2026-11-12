#!/usr/bin/env bash
# 交叉审查探针：用独立 DATA_DIR 与端口启动实例，验证 API 语义与损坏快照的影响。
# 用法：scripts/probe-api.sh [二进制路径]
# 说明：提交失败（422/500）时服务端不释放租约，探针在每次尝试后显式 DELETE，保证步骤可重复。
set -uo pipefail

BIN=/tmp/sc-probe/app
if [ "$#" -ge 1 ]; then BIN=$1; fi
PORT=18181
PROBE_PORT=
if [ -n "$PROBE_PORT" ]; then PORT=$PROBE_PORT; fi
ROOT=$(mktemp -d /tmp/sc-api-probe.XXXXXX)
DATA=$ROOT/data
TOK=TOKENRW_0123456789
ROK=TOKENR_0123456789
WK=TOKENWORK_0123456789
BASE=http://127.0.0.1:$PORT
SRV=

mkdir -p "$DATA"
cat > "$DATA/config.yaml" <<'YAML'
namespaces:
  - name: "default"
    tokens:
      - "TOKENRW_0123456789:rw"
      - "TOKENR_0123456789:r"
  - name: "work"
    tokens:
      - "TOKENWORK_0123456789:rw"
YAML

cleanup() { if [ -n "$SRV" ]; then kill "$SRV" 2>/dev/null; wait "$SRV" 2>/dev/null; fi; }
trap cleanup EXIT

DATA_DIR="$DATA" PORT="$PORT" HOST=127.0.0.1 LEASE_TTL=2 \
  AUTH_TOKEN=AUTHTOKEN_0123456789 SESSION_SIGNING_KEY=SIGNINGKEY_0123456789_0123456789 \
  "$BIN" > "$ROOT/server.log" 2>&1 &
SRV=$!
for _ in $(seq 100); do curl -sf -o /dev/null "$BASE/livez" && break; sleep 0.1; done

code() { curl -s -o "$ROOT/out" -w '%{http_code}' "$@"; }
label() { printf '\n### %s\n' "$1"; }
lease_of() { # $1=token
  curl -s -X POST -H "Authorization: Bearer $1" "$BASE/v1/lease" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("leaseId",""))'
}
newlease() { lease_of "$TOK"; }
drop_lease() { if [ -n "$1" ]; then curl -s -o /dev/null -X DELETE -H "Authorization: Bearer $TOK" "$BASE/v1/lease/$1"; fi; }
put_blob() {
  local h
  h=$(printf '%s' "$1" | sha256sum | cut -d' ' -f1)
  curl -s -o /dev/null -X PUT -H "Authorization: Bearer $TOK" --data-binary "$1" "$BASE/v1/blobs/$h"
  printf '%s' "$h"
}
head_seq() { curl -s -H "Authorization: Bearer $TOK" "$BASE/v1/snapshots?limit=1" | python3 -c 'import json,sys
d=json.load(sys.stdin).get("snapshots") or []
print(d[0]["seq"] if d else "null")'; }
try_commit() { # $1=parent 字面量  $2=manifest
  local l st
  l=$(newlease)
  if [ -z "$l" ]; then echo "(无法取得租约)"; return; fi
  st=$(code -X POST -H "Authorization: Bearer $TOK" -d "{\"leaseId\":\"$l\",\"parent\":$1,\"manifest\":$2}" "$BASE/v1/snapshots")
  echo "$st $(head -c 200 "$ROOT/out")"
  drop_lease "$l"
}

H1=$(put_blob 'body-1')
MAN="{\"files\":[{\"path\":\"a.txt\",\"hash\":\"$H1\",\"size\":6,\"mtime\":1}]}"

label "1. 租约互斥、心跳、释放、过期（LEASE_TTL=2）"
L1=$(newlease); echo "lease1=$L1"
echo "持有期间再次申请（期望 423）=> $(code -X POST -H "Authorization: Bearer $TOK" "$BASE/v1/lease")"
echo "heartbeat（期望 200）=> $(code -X POST -H "Authorization: Bearer $TOK" "$BASE/v1/lease/$L1/heartbeat") $(head -c 80 "$ROOT/out")"
echo "DELETE 释放（期望 204）=> $(code -X DELETE -H "Authorization: Bearer $TOK" "$BASE/v1/lease/$L1")"
L2=$(newlease); echo "lease2=$L2"
sleep 3
echo "过期后 heartbeat（期望 423）=> $(code -X POST -H "Authorization: Bearer $TOK" "$BASE/v1/lease/$L2/heartbeat")"
echo "过期租约提交（期望 423）=> $(code -X POST -H "Authorization: Bearer $TOK" -d "{\"leaseId\":\"$L2\",\"parent\":null,\"manifest\":$MAN}" "$BASE/v1/snapshots") $(head -c 120 "$ROOT/out")"

label "2. 首次提交 + 过期 parent"
echo "commit#1（期望 201 seq=1）=> $(try_commit null "$MAN")"
LE=$(newlease)
echo "stale parent 提交（期望 409）=> $(code -X POST -H "Authorization: Bearer $TOK" -d "{\"leaseId\":\"$LE\",\"parent\":null,\"manifest\":$MAN}" "$BASE/v1/snapshots") $(head -c 120 "$ROOT/out")"
LE2=$(newlease)
echo "409 之后立刻能拿到租约（期望非空）=> lease=$LE2"
drop_lease "$LE2"

label "3. 相同 manifest 不产生新版本"
echo "identical commit（期望 201 seq=1）=> $(try_commit 1 "$MAN")"
echo "快照文件数（期望 1）=> $(ls "$DATA/namespaces/default" | wc -l)"

label "4. manifest 校验（前四条期望 422，最后一条期望 201）"
for m in \
  "{\"files\":[{\"path\":\"a.txt\",\"hash\":\"$H1\",\"size\":6},{\"path\":\"a.txt\",\"hash\":\"$H1\",\"size\":6}]}" \
  "{\"files\":[{\"path\":\"../x\",\"hash\":\"$H1\",\"size\":6}]}" \
  "{\"files\":[{\"path\":\"a.txt\",\"hash\":\"$H1\",\"size\":7}]}" \
  "{\"files\":[{\"path\":\"a.txt\",\"hash\":\"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\",\"size\":6}]}" \
  "{\"files\":[{\"path\":\"a.txt\",\"hash\":\"$H1\",\"size\":6,\"mode\":511}]}"; do
  echo "=> $(try_commit "$(head_seq)" "$m")"
done

label "5. 认证与授权"
echo "无 token 的 /v1/metadata（期望 401）=> $(code "$BASE/v1/metadata")"
echo "r token 申请租约（期望 403）=> $(code -X POST -H "Authorization: Bearer $ROK" "$BASE/v1/lease")"
echo "r token 读 latest（期望 200）=> $(code -H "Authorization: Bearer $ROK" "$BASE/v1/snapshots/latest")"
echo "HEAD /v1/blobs（r token，期望 204）=> $(curl -s -D - -o /dev/null -I -H "Authorization: Bearer $ROK" "$BASE/v1/blobs/$H1" | sed -n '1,3p' | tr '\n' '|')"
echo "/ui 未登录（期望 302 -> /auth）=> $(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "$BASE/ui")"
echo "/ui/api/namespaces 未登录（期望 401）=> $(code "$BASE/ui/api/namespaces")"
echo "/readyz 无 token 参数（未设 READYZ_TOKEN，期望 200）=> $(code "$BASE/readyz")"

label "6. work namespace：版本保留（期望保留最近 10 个）"
SEQ=null
for i in $(seq 1 12); do
  B=w$i; H=$(put_blob "$B"); SIZE=$(printf '%s' "$B" | wc -c)
  L=$(lease_of "$WK")
  RESP=$(curl -s -X POST -H "Authorization: Bearer $WK" -d "{\"leaseId\":\"$L\",\"parent\":$SEQ,\"manifest\":{\"files\":[{\"path\":\"f$i.txt\",\"hash\":\"$H\",\"size\":$SIZE,\"mtime\":1}]}}" "$BASE/v1/snapshots")
  SEQ=$(printf '%s' "$RESP" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("seq",""))')
done
echo "head seq=$SEQ 目录=$(ls "$DATA/namespaces/work" | tr '\n' ' ')"

label "7. work 的 head 快照文件损坏（截断为 0 字节）后"
HEADFILE="$DATA/namespaces/work/$(printf '%08d' "$SEQ").jsonl"
echo "损坏文件=$HEADFILE"
: > "$HEADFILE"
echo "GET /v1/snapshots => $(code -H "Authorization: Bearer $WK" "$BASE/v1/snapshots?limit=5") $(head -c 160 "$ROOT/out")"
echo "GET /v1/snapshots/latest => $(code -H "Authorization: Bearer $WK" "$BASE/v1/snapshots/latest") $(head -c 160 "$ROOT/out")"
echo "GET /v1/metadata => $(code -H "Authorization: Bearer $WK" "$BASE/v1/metadata") $(head -c 160 "$ROOT/out")"
echo "GET /v1/snapshots/$((SEQ-1))（旧快照仍可读）=> $(code -H "Authorization: Bearer $WK" "$BASE/v1/snapshots/$((SEQ-1))") $(head -c 100 "$ROOT/out")"
LW=$(lease_of "$WK")
echo "POST /v1/snapshots => $(code -X POST -H "Authorization: Bearer $WK" -d "{\"leaseId\":\"$LW\",\"parent\":$SEQ,\"manifest\":$MAN}" "$BASE/v1/snapshots") $(head -c 160 "$ROOT/out")"
curl -s -c "$ROOT/ck" -o /dev/null -X POST -d '{"token":"AUTHTOKEN_0123456789"}' "$BASE/api/auth"
echo "GET /ui/api/namespaces => $(code -b "$ROOT/ck" "$BASE/ui/api/namespaces") $(head -c 200 "$ROOT/out")"
echo "GET /ui/api/namespaces/work/snapshots => $(code -b "$ROOT/ck" "$BASE/ui/api/namespaces/work/snapshots") $(head -c 160 "$ROOT/out")"
echo "GET /ui/api/namespaces/default/snapshots => $(code -b "$ROOT/ck" "$BASE/ui/api/namespaces/default/snapshots") $(head -c 160 "$ROOT/out")"

label "8. gc 子命令：只给 DATA_DIR（README 的用法）"
DATA_DIR="$DATA" "$BIN" gc 2>&1 | tail -1
label "9. gc 子命令：额外给 AUTH_TOKEN / SESSION_SIGNING_KEY"
DATA_DIR="$DATA" AUTH_TOKEN=AUTHTOKEN_0123456789 SESSION_SIGNING_KEY=SIGNINGKEY_0123456789_0123456789 "$BIN" gc 2>&1 | tail -1

echo
echo "DATA=$ROOT"
