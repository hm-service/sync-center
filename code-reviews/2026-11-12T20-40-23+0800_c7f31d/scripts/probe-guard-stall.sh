#!/usr/bin/env bash
# F2 复现：guarded 把 RLock 罩在整个 handler 上，serveSnapshot 在锁内把响应流给慢客户端；
# 清理（定时器或 /admin/api/gc）请求写锁时，Go 的 RWMutex 会拦下后续所有读锁请求。
# 需要一个干净的实例：本脚本在 default 里提交一个 90000 条的快照。
set -uo pipefail
BASE=${1:?usage: probe-guard-stall.sh BASE DATA_DIR}
DATA=${2:?usage: probe-guard-stall.sh BASE DATA_DIR}
RW=sync_default_rw_2026a
RO=sync_default_ro_2026a
GC=gc_token_2026_ops

echo "== 造一个 ~11 MB 的快照（90000 条）=="
python3 - "$BASE" "$RW" <<'PY'
import hashlib, json, sys, urllib.error, urllib.request
base, rw = sys.argv[1], sys.argv[2]

def call(method, path, data=None):
    request = urllib.request.Request(base + path, data=data, method=method)
    request.add_header("Authorization", "Bearer " + rw)
    try:
        with urllib.request.urlopen(request) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()

content = b"hello world"
digest = hashlib.sha256(content).hexdigest()
print("put blob", call("PUT", "/v1/blobs/" + digest, content)[0])
files = [{"path": "big/f%06d.txt" % i, "hash": digest, "size": len(content), "mtime": 0}
         for i in range(90000)]
status, raw = call("POST", "/v1/lease")
lease = json.loads(raw)["leaseId"]
status, raw = call("GET", "/v1/metadata")
head = json.loads(raw)["snapshots"][0]["seq"] if json.loads(raw)["snapshots"] else None
body = json.dumps({"leaseId": lease, "parent": head,
                   "manifest": {"files": files}}).encode()
status, raw = call("POST", "/v1/snapshots", body)
print("commit=%d %s payload=%d bytes" % (status, raw.strip(), len(body)))
PY

SEQ=$(curl -s -H "Authorization: Bearer $RO" "$BASE/v1/metadata" |
  python3 -c 'import json,sys;print(json.load(sys.stdin)["snapshots"][0]["seq"])')
SIZE=$(stat -c %s "$DATA/namespaces/default/$(printf '%08d' "$SEQ").jsonl")
echo "head seq = $SEQ ($SIZE bytes)"

echo "== 一个慢客户端以 20 KB/s 读该快照 =="
curl -s --limit-rate 20k -o /dev/null -w 'slow-read=%{http_code} bytes=%{size_download} t=%{time_total}\n' \
  -H "Authorization: Bearer $RO" "$BASE/v1/snapshots/$SEQ" > "$DATA/slowread.log" 2>&1 &
SLOW=$!
sleep 1.5

echo "== 触发 GC：应立刻返回，实际在等写锁 =="
curl -s -m 8 -o /dev/null -w 'gc=%{http_code} t=%{time_total}\n' -X POST -H "Authorization: Bearer $GC" "$BASE/admin/api/gc"
echo "== GC 排队期间 /v1/metadata：新的读锁被挂起的写者挡住 =="
curl -s -m 5 -o /dev/null -w 'metadata=%{http_code} t=%{time_total}\n' -H "Authorization: Bearer $RO" "$BASE/v1/metadata"
echo "== 再次触发 GC：单飞生效，返回 423 =="
curl -s -m 3 -o /dev/null -w 'gc2=%{http_code} t=%{time_total}\n' -X POST -H "Authorization: Bearer $GC" "$BASE/admin/api/gc"

kill "$SLOW" 2>/dev/null
wait "$SLOW" 2>/dev/null
sleep 1
echo "== 慢客户端断开后 GC 立刻完成 =="
curl -s -m 10 -w '\ngc3=%{http_code} t=%{time_total}\n' -X POST -H "Authorization: Bearer $GC" "$BASE/admin/api/gc"
echo

echo "== 变体：提交请求体慢慢发，guarded 在锁内读 body（不需要有效租约即可占住锁）=="
python3 - "$BASE" "$RW" "$DATA" <<'PY' &
import socket, sys, time
base, rw, data = sys.argv[1], sys.argv[2], sys.argv[3]
body = b'{"leaseId":"x","parent":null,"manifest":{"files":[]}}'
sock = socket.create_connection(("127.0.0.1", int(base.rsplit(":", 1)[1])))
head = ("POST /v1/snapshots HTTP/1.1\r\nHost: 127.0.0.1\r\nAuthorization: Bearer %s\r\n"
        "Content-Type: application/json\r\nContent-Length: %d\r\n\r\n") % (rw, len(body))
sock.sendall(head.encode())
sock.sendall(body[:10])
open(data + "/slowbody.started", "w").write("1")
time.sleep(6)
sock.close()
PY
SLOWBODY=$!
for _ in $(seq 50); do [ -f "$DATA/slowbody.started" ] && break; sleep 0.1; done
curl -s -m 4 -o /dev/null -w 'gc-during-slow-body=%{http_code} t=%{time_total}\n' -X POST -H "Authorization: Bearer $GC" "$BASE/admin/api/gc"
kill "$SLOWBODY" 2>/dev/null
wait "$SLOWBODY" 2>/dev/null
rm -f "$DATA/slowbody.started"
