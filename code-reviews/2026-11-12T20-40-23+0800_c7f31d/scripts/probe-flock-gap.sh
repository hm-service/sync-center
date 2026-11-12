#!/usr/bin/env bash
# F3 复现：blob 存在性校验在 Commit 的共享 flock 之外，gc 在“校验完成、进 Commit 之前”
# 删掉 blob，提交仍然成功，写出引用悬空 blob 的不可变快照。
# 这里用外部进程持独占 flock + 手工 rm 来精确制造这个交错（与 gc 子命令独占段内删除等价）。
set -uo pipefail
BASE=${1:?usage: probe-flock-gap.sh BASE DATA_DIR}
DATA=${2:?usage: probe-flock-gap.sh BASE DATA_DIR}
RW=sync_default_rw_2026a
TMP=$(mktemp -d /tmp/sc-flock-gap.XXXXXX)
trap 'rm -rf "$TMP"' EXIT

python3 - "$BASE" "$RW" "$TMP" <<'PY'
import hashlib, json, pathlib, sys, urllib.error, urllib.request
base, rw, tmp = sys.argv[1], sys.argv[2], sys.argv[3]
call = lambda m, p, d=None, t=rw: urllib.request.urlopen(
    urllib.request.Request(base + p, data=d, method=m,
                           headers={"Authorization": "Bearer " + t})).read()
content = b"flock-victim-content"
digest = hashlib.sha256(content).hexdigest()
call("PUT", "/v1/blobs/" + digest, content)
lease = json.loads(call("POST", "/v1/lease"))["leaseId"]
metas = json.loads(call("GET", "/v1/metadata"))["snapshots"]
head = metas[0]["seq"] if metas else None
pathlib.Path(tmp + "/victim.json").write_text(json.dumps(
    {"hash": digest, "lease": lease, "head": head, "size": len(content)}))
print("victim", digest, "head", head)
PY
H=$(python3 -c "import json;print(json.load(open('$TMP/victim.json'))['hash'])" )
echo "victim blob = $H"

echo "== 外部进程持独占 flock（模拟 gc 子命令的独占段，6 秒）=="
python3 - "$DATA/.lock" "$TMP" <<'PY' &
import fcntl, os, sys, time
handle = open(sys.argv[1], "r+")
fcntl.flock(handle, fcntl.LOCK_EX)
open(os.path.join(sys.argv[2], "held"), "w").write("1")
time.sleep(6)
fcntl.flock(handle, fcntl.LOCK_UN)
PY
GCPID=$!
for _ in $(seq 50); do [ -f "$TMP/held" ] && break; sleep 0.1; done
echo "exclusive lock held"

echo "== 并发提交：校验 blob 通过后在 Commit 里等 flock =="
python3 - "$BASE" "$RW" "$TMP" > "$TMP/commit.out" 2>&1 <<'PY' &
import json, sys, time, urllib.error, urllib.request
base, rw, tmp = sys.argv[1], sys.argv[2], sys.argv[3]
info = json.load(open(tmp + "/victim.json"))
body = json.dumps({"leaseId": info["lease"], "parent": info["head"],
                   "manifest": {"files": [{"path": "victim.txt", "hash": info["hash"],
                                           "size": info["size"], "mtime": 0}]}}).encode()
request = urllib.request.Request(base + "/v1/snapshots", data=body, method="POST",
                                 headers={"Authorization": "Bearer " + rw})
started = time.time()
try:
    with urllib.request.urlopen(request) as response:
        print("commit", response.status, response.read(), "elapsed", round(time.time() - started, 2))
except urllib.error.HTTPError as exc:
    print("commit", exc.code, exc.read(), "elapsed", round(time.time() - started, 2))
PY
CPID=$!
sleep 1.5
BLOB="$DATA/blobs/$(echo "$H" | cut -c1-2)/$(echo "$H" | cut -c3-4)/$H"
echo "提交已通过校验、正等锁；blob 存在？$([ -f "$BLOB" ] && echo yes || echo no)"
echo "== 模拟 gc 在独占段内删掉这个未被任何快照引用的 blob =="
rm -f "$BLOB"
wait "$CPID"; cat "$TMP/commit.out"
wait "$GCPID" 2>/dev/null
rm -f "$TMP/held"

echo "== 结果：新快照引用了已删除的 blob =="
curl -s -H "Authorization: Bearer $RW" "$BASE/v1/snapshots/latest"; echo
echo "GET  blob => $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $RW" "$BASE/v1/blobs/$H")"
echo "HEAD blob => $(curl -s -I -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $RW" "$BASE/v1/blobs/$H")"
