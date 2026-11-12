# 评审响应记录

| 项 | 值 |
| --- | --- |
| 评审记录 | [REVIEW.md](./REVIEW.md)，目录 `2026-11-12T20-40-23+0800_c7f31d`，对象 `c7f31d3` |
| 响应时间 | 2026-11-12T21:07+0800（Asia/Shanghai，UTC+8） |
| 响应范围 | 5 条 findings（3×P2 + 2×P3）全部处理；8 条待确认项逐条说明 |

## F1 [P2] 登录把同一个 `authGate` 取了两次

- 位置：[api/router.go](../../api/router.go)、[api/auth.go](../../api/auth.go)
- 做法：删掉 `/api/auth` 外层的 `s.gate(s.authGate, ...)`，只保留 `login` 内显式的取/放——恒定 1 秒等待本来就在槽位之外。容量 2 现在真的是 2 个并发登录，第 3 个排队而不是互等超时。
- 验证：`scripts/probe-login.sh` 用新二进制重跑，2 并发 × 4 轮全部 `200 @1.02–1.19 s`，5 并发全部 `200 @1.01–1.16 s`（修复前是 `3×503 @10.00 s`）。
- 回归测试：`api.TestConcurrentLoginsAllSucceed`，3 并发断言全部 200 且耗时远低于 `gateWait`。

## F2 [P2] 读锁罩住整个响应流与请求体，回收的写锁等待不可中断

- 位置：[api/router.go](../../api/router.go)、[app/gc.go](../../app/gc.go)
- 做法：把"暂停读锁"收缩到毫秒级的元数据接口——lease 三个接口、快照列表、`/v1/metadata`、面板的 namespace 与快照列表。快照内容、blob 下载、提交请求体的读写不再参与暂停：`GET /v1/snapshots/{seq}`、`POST /v1/snapshots`、`GET /admin/api/namespaces/{name}/snapshots/{seq}` 只过并发闸门，理由与 blob 下载相同（已打开的文件 unlink 后仍能读完）。提交与回收的互斥改由 `Commit` 的共享 flock 保证（见 F3），不再依赖请求读锁。
- 效果：客户端无法再决定回收等多久。清理期间真正会排队的只剩元数据请求，持有时间由服务端决定。
- 验证：`scripts/probe-guard-stall.sh`（11 880 053 字节快照、20 KB/s 慢读）：GC `200 @0.071 s`，同期 `/v1/metadata` `0.0006 s`；慢请求体变体 GC `200 @0.077 s`。修复前分别是 8 s 超时、5 s 超时。
- 未加写超时：客户端已经不能让回收等待，再给大文件下载加写超时只会伤到合法传输，故不动。

## F3 [P2] blob 校验在共享 flock 之外

- 位置：[core/snapshots.go](../../core/snapshots.go)（`Commit`）、[api/v1.go](../../api/v1.go)（`blobCheck`）
- 做法：`SnapshotStore.Commit` 增加 `verify func() error` 参数，在共享 flock 内、parent 校验之后、写临时文件之前执行。`createSnapshot` 的"blob 仍在且大小一致"搬进这个回调，失败仍映射 `422 missing_blob` / `blob_size_mismatch`（新增 `blobCheckError` 只负责带出错误码）。校验与落盘现在处于同一个 flock 区间，外部 `gc` 无法插进中间。
- 验证：`scripts/probe-flock-gap.sh` 重跑，外部独占 flock 期间删掉被引用 blob，提交返回 `422 missing_blob`（等锁 5.86 s 后拒绝），`latest` 仍是 `no snapshot yet`——不再产生引用悬空的快照。
- 回归测试：`core.TestCommitRunsVerifierBeforeWriting`（校验失败不留快照）、`core.TestCommitHoldsSharedLockDuringVerification`（校验期间独占锁拿不到，提交结束后立即拿到）。

## F4 [P3] 损坏快照让回收整体失败，HTTP 只回 `internal`

- 位置：[core/snapshots.go](../../core/snapshots.go)（`DamagedError`）、[api/admin.go](../../api/admin.go)、[i18n/i18n.go](../../i18n/i18n.go)
- 做法：`EachManifest` 返回 `*core.DamagedError{Namespace, File}`；`POST /admin/api/gc` 把它映射成 `409 {"error":"damaged_snapshot","message":"... work/00000002.jsonl"}`（新 key `error.damaged-snapshot`，中英各一条）。子命令行为不变，仍 exit 1 并打印同一句。
- 未做：在 `/readyz` 或面板里暴露损坏文件、`GC_ALLOW_DAMAGED` 放行开关。前者要动面板接口契约（DESIGN 第 6 节），后者是产品决策，先作为建议留给用户。
- 验证：`scripts/probe-damaged-gc.sh` → `409` + 文件名；同一数据目录上 `gc` 子命令 exit 1。再提交一个版本后仍是 409（符合"有损坏就拒绝"的既定策略）。
- 回归测试：`api.TestCollectGarbageReportsDamagedSnapshot`、`core.TestEachManifestReportsDamagedFile`。

## F5 [P3] `GC_INTERVAL=0` 与"需要 GC_TOKEN"叠加后没有回收路径

- 位置：[main.go](../../main.go)、[README.md](../../README.md)
- 做法：启动时对 `GC_INTERVAL=0 && !GCEnabled()` 打一条 WARN（`garbage collection is disabled`）；README 改成"设成 0 只保留手动触发（手动触发要求 `GC_TOKEN`，两者都没配时只剩停服时的 `gc` 子命令）"。
- 验证：18097 实例上 `GC_INTERVAL=0`、无 `GC_TOKEN`，两条日志都在（`gc api disabled` + WARN）。
- DESIGN 未改：第 10 节那行还写着"只响应 `POST /admin/api/gc`"，建议补"GC_TOKEN 为空时该接口不注册"（见文末）。

## 待确认项的处理

| # | 项 | 本轮处理 |
| --- | --- | --- |
| 1 | 回收引用集内存上界 | 未改。需要先用满配数据实测，再决定是否换成 `map[[32]byte]struct{}` 或按 namespace 分段回收 |
| 2 | `prune` 的 unlink 未 fsync 目录 | 未改。改一行就有，但会为每个被裁剪的文件多一次目录 fsync，成本与收益要用户拍板 |
| 3 | `syncDir` 失败时数据已落地 | 未改。只在文件系统不支持目录 fsync 时出现，当前部署碰不到 |
| 4 | 非 unix 构建静默退化成空锁 | 未改。只发布 linux 产物 |
| 5 | `adminSnapshot` 不支持 `latest` | 未改。面板只用数字 seq，属接口不对称，记录在案 |
| 6 | `readyz` 的 `.probe-*` 残留 | 未改。可在 `Collect` 里顺手清 `namespaces/.probe-*`，等用户决定 |
| 7 | 同一 `DATA_DIR` 两个进程 | 未改。需要启动期进程锁，属于部署模型决策；它同时能让 `gc` 子命令拒绝在线运行，建议下一轮做 |
| 8 | 2 并发登录的竞态窗口 | 已随 F1 消失；回归基线用 3 并发（更快、更稳定） |

## 构建与测试

| 命令 | 结果 |
| --- | --- |
| `just check` | 通过（`go vet`、`go test`、`pnpm install --frozen-lockfile`、`vue-tsc -b`） |
| `just build` | 通过，amd64 7.81 MB / arm64 7.14 MB，`ldd` 报 `not a dynamic executable` |
| `go test ./... -count=1` | `core` ok、`api` ok；`=== RUN` 41 个（原 36 + core 3 个新测试 + api 2 个新测试） |
| 协议冒烟 `../sync-center/tests/smoke.py` | 34 项全 PASS（18094，全新 `DATA_DIR`） |
| 探针 `scripts/probe-*.sh` | 用新二进制重跑，F1–F4 均已消失（输出见各条） |
| 面板接口 | 会话登录后 `/admin`、快照列表、快照详情（head seq）均 200 |

未验证：断电注入、`sendfile` 的 syscall 级观察、`>64 MiB` 请求体的 413 重测、arm64 产物实机运行。

## 需要用户决定的文档改动（未擅自修改）

工作区的 `DESIGN.md` 有用户尚未提交的编辑，本轮**未改动也未提交**。按当前实现，建议补两句：

1. 第 8 节"清理持写锁，请求持读锁……blob 传输与登录不参与" → 快照内容与提交请求体现在也不参与，建议改成："清理持写锁，租约、快照列表、元信息与面板列表接口会排队等待，通常只有毫秒级；blob 传输、快照内容读取、提交请求体与登录不参与（已打开的文件 unlink 后仍可读完）。"
2. 第 10 节 `GC_INTERVAL` 行尾补："`GC_TOKEN` 为空时该接口不注册，此时只剩 `gc` 子命令。"

## 后续

用户在响应之后指示更新文档：DESIGN 第 6/8/10 节已按上面的建议修改（同时把"运维"表移到状态码之后），与代码保持一致。
