#!/usr/bin/env bash
# F1 复现：登录路径自己再取一次 authGate，而外层 gate 已经占了同一个 channel 的槽位。
# 容量为 2，两个并发登录各拿一个外层槽位后互相等待内层槽位 → 10 秒后双双 503。
set -uo pipefail
BASE=${1:?usage: probe-login.sh BASE}
TOKEN=${AUTH_TOKEN:-sync_center_panel}

echo "== 单个登录 =="
curl -s -o /dev/null -w 'single=%{http_code} t=%{time_total}\n' -X POST "$BASE/api/auth" -d "{\"token\":\"$TOKEN\"}"

echo "== 2 个并发登录 × 4 轮（期望各 200、约 1 秒，正确实现下可用并发预算为 2）=="
for round in 1 2 3 4; do
  for i in 1 2; do
    ( curl -s -o /dev/null -w "round$round-login$i=%{http_code}@%{time_total} " -X POST "$BASE/api/auth" -d "{\"token\":\"$TOKEN\"}" ) &
  done
  wait
  echo
done

echo "== 5 个并发登录（期望：全部 200；实际：多数 503，耗时 10 秒）=="
for i in 1 2 3 4 5; do
  ( curl -s -o /dev/null -w "login$i=%{http_code}@%{time_total} " -X POST "$BASE/api/auth" -d "{\"token\":\"$TOKEN\"}" ) &
done
wait
echo
