#!/usr/bin/env bash
# F4 复现：任意 namespace 里有一个无法解析的快照，全数据目录的 blob 回收就整体失败；
# 只有该文件被 prune（再提交 10 个版本）或手工删除后才恢复。
set -uo pipefail
BASE=${1:?usage: probe-damaged-gc.sh BASE DATA_DIR}
DATA=${2:?usage: probe-damaged-gc.sh BASE DATA_DIR}
WORK=sync_work_rw_2026a
GC=gc_token_2026_ops

echo "== 在 work 里造 3 个快照 =="
python3 - "$BASE" "$WORK" <<'PY'
import hashlib, json, sys, urllib.error, urllib.request
base, work = sys.argv[1], sys.argv[2]

def call(method, path, data=None):
    request = urllib.request.Request(base + path, data=data, method=method,
                                     headers={"Authorization": "Bearer " + work})
    try:
        with urllib.request.urlopen(request) as response:
            return response.status, response.read()
    except urllib.error.HTTPError as exc:
        return exc.code, exc.read()

for i in range(3):
    content = ("payload-work-%d" % i).encode()
    digest = hashlib.sha256(content).hexdigest()
    print("put", call("PUT", "/v1/blobs/" + digest, content)[0])
    lease = json.loads(call("POST", "/v1/lease")[1])["leaseId"]
    metas = json.loads(call("GET", "/v1/metadata")[1])["snapshots"]
    parent = metas[0]["seq"] if metas else None
    body = json.dumps({"leaseId": lease, "parent": parent, "manifest": {"files": [
        {"path": "w%d.txt" % i, "hash": digest, "size": len(content), "mtime": 0}]}}).encode()
    print("commit", call("POST", "/v1/snapshots", body))
PY

echo "== 损坏中间版本 00000002.jsonl =="
printf 'garbage\n' > "$DATA/namespaces/work/00000002.jsonl"
echo "list     => $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $WORK" "$BASE/v1/snapshots?limit=10")"
echo "metadata => $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $WORK" "$BASE/v1/metadata")"
echo "seq 3    => $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $WORK" "$BASE/v1/snapshots/3")"

echo "== 回收（HTTP）=="
curl -s -w '\ngc=%{http_code}\n' -X POST -H "Authorization: Bearer $GC" "$BASE/admin/api/gc"
echo "== 回收（子命令，错误信息更具体）=="
DATA_DIR="$DATA" "${APP:-/tmp/sc-review-c7f31d/app}" gc; echo "exit=$?"

echo "== 再提交一个版本，回收仍然失败 =="
python3 - "$BASE" "$WORK" <<'PY'
import hashlib, json, sys, urllib.request
base, work = sys.argv[1], sys.argv[2]
call = lambda m, p, d=None: urllib.request.urlopen(
    urllib.request.Request(base + p, data=d, method=m,
                           headers={"Authorization": "Bearer " + work})).read()
content = b"after-damage"
digest = hashlib.sha256(content).hexdigest()
call("PUT", "/v1/blobs/" + digest, content)
lease = json.loads(call("POST", "/v1/lease"))["leaseId"]
parent = json.loads(call("GET", "/v1/metadata"))["snapshots"][0]["seq"]
body = json.dumps({"leaseId": lease, "parent": parent, "manifest": {"files": [
    {"path": "z.txt", "hash": digest, "size": len(content), "mtime": 0}]}}).encode()
print("commit", call("POST", "/v1/snapshots", body))
PY
curl -s -o /dev/null -w 'gc-after-commit=%{http_code}\n' -X POST -H "Authorization: Bearer $GC" "$BASE/admin/api/gc"
echo "work 目录："; ls "$DATA/namespaces/work/"
