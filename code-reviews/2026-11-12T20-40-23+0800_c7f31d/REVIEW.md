# 代码审查记录

| 项 | 值 |
| --- | --- |
| 审查对象 | `c7f31d3bc2bcfe6304e471f73358e8905257ffd5`（短 SHA `c7f31d3`） |
| 分支 | `main` |
| 审查时间 | 2026-11-12T20:40:23+0800（Asia/Shanghai，UTC+8） |
| 审查目录 | `code-reviews/2026-11-12T20-40-23+0800_c7f31d/` |
| 上一轮审查 | `code-reviews/2026-11-12T19-51-05+0800_9c52bc/`（提交 `9c52bc2`） |
| 开始审查时工作区 | `git status --porcelain` 为 ` M DESIGN.md`。**DESIGN.md 是用户正在编辑的未提交改动（`git diff --stat` 显示 12 增 12 删），按约定排除在本轮评审之外**：未纳入评审范围，也不评论其内容；本文件所有 DESIGN 引用都取自已提交的 `git show HEAD:DESIGN.md`，行号也按 HEAD 版本计 |
| 结束审查时工作区 | 仅新增未跟踪目录 `code-reviews/2026-11-12T20-40-23+0800_c7f31d/`；生产代码、测试代码、DESIGN.md 均未改动，未提交 |
| 本轮提交范围 | `9c52bc2..HEAD` 共 5 个提交，全部在范围内（见下） |

```
c7f31d3 go fix 现代化字符串与切片遍历 gofmt 修正多余空行 401 响应补 WWW-Authenticate 页面提供 favicon，删除模板遗留的图标精灵
ca163c7 面板路径改为 /admin，GC 接口改用 GC_TOKEN
c5f32cf 增加 POST /v1/gc 运维接口
ae59e26 垃圾回收改为服务内定时执行
4bcc7dd 修复首轮交叉审查的问题
```

## 审查范围

- 文档基准：`DESIGN.md`（HEAD 版本，14 节）、`README.md`、`config.example.yaml`、`k8s.sync-center.yaml`；
- Go 代码：`main.go`、`web.go`、`api/{router,auth,admin,v1}.go`、`app/{app,lease,gc}.go`、`core/{config,manifest,snapshots,blob,token,session,fs,lock_unix,lock_other}.go`、`httpx/{httpx,clientip}.go`、`i18n/i18n.go`；
- 测试代码：`core/{config_test,snapshots_test}.go`（只读）；
- 前端：`frontend/{index.html,public/favicon.svg}`、`frontend/src/**`、`vite.config.ts`；
- 构建与部署：`justfile`、`Dockerfile`、`.dockerignore`、`k8s.sync-center.yaml`、`.github/workflows/`；
- 变更历史：`git diff 9c52bc2..HEAD` 全量（34 个文件，含 5 个新增文件）；对照了 `../sync-center` 的 Python 原型（协议与 `gc` 语义）。

本轮新增/重写的部分主要是：进程内定时回收 + `POST /admin/api/gc` + `GC_TOKEN`、`/ui` → `/admin` 改名、`app.Data` + `Guard` 读写锁 + `flock`、损坏快照的跳过与 `DamagedAhead`、`fsync`、`gate` 超时与登录独立配额、`recorder.Unwrap/ReadFrom/Flush`、favicon 与 `WWW-Authenticate`、`go fix` 现代化（`slices.Backward`、`strings.SplitSeq`、`new(value)`）。

## 上一轮修复复核（`git diff 9c52bc2..HEAD`）

| # | 上轮结论 | 本轮复核 | 说明 |
| --- | --- | --- | --- |
| F1 | 登录独立配额 | **部分成立，且引入新缺陷** | `/v1` 不再被登录风暴阻塞（实测登录风暴期间 `GET /v1/metadata` 0.0007 s）；但 `/api/auth` 外层 `gate(authGate)` 与 `login` 内部又取了一次同一个闸门 → 见本轮 F1 |
| F2 | 跳过损坏文件 | 成立 | 实测：head 截断后列表 200、面板 200、旧版本 200，`latest`/`metadata`/该 seq 500；提交以同 seq 覆盖损坏文件后自愈（201）。副作用见本轮 F4 |
| F3 | 落盘 fsync | 成立 | `tmp.Sync()` + rename 后 `syncDir` 都在（[core/snapshots.go:305-322](../../core/snapshots.go#L305-L322)、[core/blob.go:64-90](../../core/blob.go#L64-L90)）；未做断电注入。`prune` 的 unlink 未 fsync，见待确认 2 |
| F4 | README + sync.db 拒绝 | 成立 | `gc` 子命令在数据目录存在 `sync.db` 时退出 1：`refusing to delete blobs`；README 已声明两种布局不能共用 |
| F5 | gc 与提交互斥 | **部分成立，存缺口** | 进程内 `Guard` 互斥实测有效；但 `flock` 只罩住 `Commit` 内部，blob 校验在锁外 → 见本轮 F3 |
| F6 | 裁剪导致的 ENOENT 跳过 | 成立 | `List` 对 `fs.ErrNotExist` 直接跳过（[core/snapshots.go:108-115](../../core/snapshots.go#L108-L115)）；微秒窗口未确定性复现 |
| F7 | 闸门 ctx/超时、IdleTimeout | 成立 | `gate` 改为 `select` + 10 s 超时；`IdleTimeout=120s`。本轮 F2 是另一个原因导致的挂起（写者等待 + 无写超时），不是原 F7 回退 |
| F8 | 已存在 blob 的排空上限 | 成立 | 该分支套了 `MaxBytesReader(MaxBlobSize+1)` 并把超限映射到 413（[api/v1.go:84-97](../../api/v1.go#L84-L97)）；本轮未重跑 257 MiB |
| F9 | HEAD 的 Content-Length | 成立 | 实测 `HEAD /v1/blobs/{hash}` 为 204 且无 Content-Length |
| F10 | gc 子命令免面板密钥 | 成立 | 实测只给 `DATA_DIR` 即 `garbage collected` exit 0；未知子命令 exit 1 |
| F11 | 重复 token 拒绝 | 成立 | `NewTokenIndex` 命中即报错；`TestTokenIndexRejectsDuplicates` 通过 |
| F12 | 服务端 `ui.*` 删除 | 成立 | `grep '"ui\.' --include=*.go` 为 0；服务端用到的 `error.*` 与词典定义完全对齐（无缺、无多） |
| F13 | 死代码删除 | 成立 | `core.Parse`、`errMissingSecret` grep 为 0 |
| F14 | `recorder` 转发 | 成立 | `Unwrap`/`ReadFrom`/`Flush` 已加（[api/router.go:266-286](../../api/router.go#L266-L286)）；无 strace，未直接观察 `sendfile` |
| F15 | 文档失效 | 成立 | `docs/model.svg` 已入库，DESIGN 链接指向 `docs/model.svg`；README token 示例长度合法 |
| F16 | 前端 `?seq=` 与预览错误 | 成立 | 代码已加 `Number.isInteger` 守卫与 `response.ok` 判断；浏览器截图面板正常 |

## 实际执行的构建与测试

| 命令 | 结果 |
| --- | --- |
| `go version` / `node -v` / `pnpm -v` | go1.27.3 linux/amd64、v26.13.0、11.28.5 |
| `go build ./...` | 通过（exit 0） |
| `go vet ./...` | 通过（exit 0） |
| `gofmt -l .` | 无输出 |
| `go test ./... -count=1` | 通过；`core` ok（13 个顶层测试、36 个 RUN 全部 PASS），其余包无测试 |
| `just check` | 通过（`go vet`、`go test`、`pnpm install --frozen-lockfile`、`vue-tsc -b`），exit 0 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/.../app-arm64 .` | 通过；`file` 为 `ELF 64-bit ... ARM aarch64 ... statically linked`；`ldd` 报 `not a dynamic executable` |
| 协议冒烟 `../sync-center/tests/smoke.py`（只读使用，`sed` 把 BASE 改到 18094） | 34 项全部 PASS（租约、blob、快照、409/423/422、版本保留、namespace 隔离、metadata） |
| `bash scripts/run-all.sh 18099`（全新 `DATA_DIR`） | 4 个探针全部复现本轮 F1–F4（F5 是配置/文档语义，无需探针），输出见下 |
| 浏览器：`/auth`、`/admin?ns=default&seq=14`（Cookie 注入会话） | 登录页与面板正常渲染，无 console 报错；面板正确列出 2 个 namespace、10 个版本、文件树与预览列 |
| `DATA_DIR=<dir> /tmp/review-c7f31d/app gc` | `garbage collected removed=0` exit 0（不需要 AUTH_TOKEN / SESSION_SIGNING_KEY）；`sync.db` 存在时 exit 1 |

探针实例全部使用空闲端口（18094–18099）与全新 `DATA_DIR`，退出时已 kill；18080 上的主服务全程未作为探针目标。

## Findings

### [P2][F1] 登录把同一个 `authGate` 取了两次，并发登录互相等待 10 秒后返回 503

- 位置：[api/router.go:66](../../api/router.go#L66)（外层 `s.gate(s.authGate, ...)`）、[api/router.go:22-25](../../api/router.go#L22-L25)（`authSlots = 2`）、[api/auth.go:26-33](../../api/auth.go#L26-L33)（`login` 内部再取一次）、[api/auth.go:41](../../api/auth.go#L41)（释放内层）。
- 事实：`/api/auth` 被 `s.gate(s.authGate, ...)` 包了一层，`login` 的第一段又对**同一个 channel** 做一次 `s.authGate <- struct{}{}`。容量只有 2：两个并发请求各拿走一个外层槽位后，两个内层获取都拿不到槽位，只能一起等到 `gateWait=10s` 超时返回 503。DESIGN 与代码注释都写着"登录并发配额 2"（[DESIGN.md:156](../../DESIGN.md#L156)、[DESIGN.md:236](../../DESIGN.md#L236)），实际可用并发是 1。
- 触发条件：≥2 个 `POST /api/auth` 在几百微秒内同时到达（两个浏览器同时登录、脚本重试、双击提交都可能命中）。
- 实际结果（`scripts/probe-login.sh`，18099，全新实例）：
  - 单个登录：`200 @1.12s`；
  - 2 个并发 × 4 轮：第 3 轮**两个都 503 @10.01s**，其余轮次各 200 @1.0–1.2s（同一探针在 18094 上 5 轮里有 3 轮双双 503，说明是稳定的时序竞态而不是偶发）；
  - 5 个并发：`3×503 @10.00s` + `2×200`，其中一个成功请求耗时 **10.05s**——它在闸门前被卡了 10 秒。
  - 注：登录风暴期间 `GET /v1/metadata` 仍 0.0007s，说明上一轮"登录不挤占 /v1"的目标已经达成；坏掉的是登录自身的并发预算。
- 修复建议：二选一，别叠加。① 删掉 [api/router.go:66](../../api/router.go#L66) 的外层 `gate`，只保留 `login` 内显式的取/放（这正是上轮 F1 的本意，且能把 1 秒恒定等待放在槽位外）；② 删掉 `login` 内部的第二次获取，只保留外层——但这样 1–1.2 秒的恒定等待仍然占着外层槽位，2 个槽位只够 2 个并发登录串行等待，建议把 `authSlots` 提到 3 或把等待彻底移出槽位。修复后补一个"两个并发登录都必须 200 且都在 ~1s 内"的回归测试。

### [P2][F2] 读锁罩住了整个响应流与请求体读取，清理的写锁等待又不可中断：一个慢客户端就能让所有"暂停敏感"接口无限期挂起

- 位置：[api/router.go:194-202](../../api/router.go#L194-L202)（`guarded` 在 `next.ServeHTTP` 前后整体持 `RLock`）、[api/v1.go:309-326](../../api/v1.go#L309-L326)（`serveSnapshot` 在 handler 里把快照流式写给客户端）、[app/gc.go:18-36](../../app/gc.go#L18-L36)（`CollectGarbage`/`TryCollectGarbage` 里阻塞的 `d.Guard.Lock()`）、[main.go:107-131](../../main.go#L107-L131)（定时器每天自动跑一次，启动时也跑）、[main.go:67-72](../../main.go#L67-L72)（只有 `ReadHeaderTimeout`/`IdleTimeout`，没有 `WriteTimeout`）。
- 事实：`Guarded` 把 `RLock` 包在整个 handler 外层，而 `serveSnapshot`、`createSnapshot` 的耗时部分（把 10+ MB 响应写给慢客户端、在锁内读最多 64 MiB 的提交请求体）都在锁内。Go 的 `sync.RWMutex` 在写者排队后会拦下新的 `RLock`，所以清理一开始等写锁，随后到达的所有 `/v1`（lease/snapshots/metadata）与 `/admin/api/namespaces*` 请求全部排在写者后面。清理的 `Guard.Lock()` 本身没有超时也不能取消，而慢客户端没有写超时兜底，完全由客户端决定什么时候释放锁。
- 触发条件：一个合法客户端（**只读 token 就够**）在 `GET /v1/snapshots/{seq}` 上以极低速率读，或一个 `POST /v1/snapshots` 的请求体只发 1 字节/秒；此时进程内定时回收（每天一次，或启动时）、`POST /admin/api/gc` 或 `gc` 子命令触发清理。
- 实际结果（`scripts/probe-guard-stall.sh`，18099）：
  - 以 20 KB/s 读 11.88 MB 的快照（`GET /v1/snapshots/2`），随后 `POST /admin/api/gc`：**8s 客户端超时，GC 一直没返回**（`gc=000 t=8.002`）；
  - GC 排队期间 `GET /v1/metadata`：**5s 超时**（`metadata=000 t=5.003`）——新的读请求被挂起的写者挡住；
  - 第二次 GC 拿到 `423`（第一次仍持有 `gcMu`），杀掉慢客户端后 GC `200 @0.083s`；
  - 慢速请求体变体（`Content-Length` 已发、body 只发 10 字节）：`gc-during-slow-body=000 t=4.002`，同样拿不到写锁。
  - 只读 token 即可触发，且协议里没有"取消下载"的约束：这是一条由低权限凭据发起的、可无限维持的全局停顿。
- 与文档的偏差：DESIGN 说"清理期间 …… 会排队等待，通常只有毫秒级"（[DESIGN.md:216](../../DESIGN.md#L216)）、README 说"清理期间请求会短暂排队"（[README.md:62](../../README.md#L62)），在存在慢客户端时不成立。
- 修复建议（按直接程度排序）：① 缩小读锁范围——只在"列目录 / 打开文件 / 解析元数据"时持锁，把流式写出与请求体读取放到锁外（本仓库已经用"unlink 后已打开的文件仍可读"这个理由让 blob 下载不参与暂停，快照读取没有理由不同）；② 清理改用 `Guard.TryLock()`：拿不到就跳过本轮（定时器下一轮再来、HTTP 返回 423/503），永不阻塞等待；③ 给大响应设置写超时（`http.ResponseController.SetWriteDeadline`）、给提交请求体设置读超时，让"慢客户端"有上界；④ 被跳过的清理必须打日志，否则 blob 会静默堆积。

### [P2][F3] blob 校验在共享 flock 之外，外部 `gc` 子命令仍能造出引用缺失 blob 的不可变快照（上轮 F5 的缺口）

- 位置：[api/v1.go:177-187](../../api/v1.go#L177-L187)（逐个 `Blobs.Stat` 校验，此时没有任何锁）、[api/v1.go:189](../../api/v1.go#L189)（之后才调用 `Commit`）、[core/snapshots.go:230-235](../../core/snapshots.go#L230-L235)（共享 `flock` 在 `Commit` 内部才取）、[app/gc.go:44-62](../../app/gc.go#L44-L62)（独占 `flock` 期间扫描引用并删除 blob）、[DESIGN.md:218](../../DESIGN.md#L218)（"外部 gc 子命令 …… 与提交通过 `DATA_DIR/.lock` 的 `flock` 互斥"）。
- 事实：`createSnapshot` 的校验（blob 存在 + 大小一致）发生在 handler 里，**不持 flock**；`Commit` 才取共享锁。所以"校验完成 → 进入 Commit 取锁"之间有一个窗口，独占段的 gc 可以在这个窗口里把该 blob 当成"无人引用"删掉（blob 的 mtime 早于 1 小时即满足删除条件）。上轮 [RESPONSE.md](../2026-11-12T19-51-05+0800_9c52bc/RESPONSE.md) 断言"客户端先传 blob、很久以后才提交的场景，提交会因为 blob 不存在而返回 422，不会产生引用缺失的快照"，这个断言只对"删除发生在校验之前"成立。
- 触发条件：服务运行期间执行 `DATA_DIR=... app gc`（DESIGN 说它用于停服维护，但没有任何机制阻止在线运行，而且上面的 flock 描述会让人以为在线也安全），并且恰好有一个"blob 已校验、尚未进 Commit"的提交。
- 实际结果（`scripts/probe-flock-gap.sh`，18099；探针用外部进程持独占 flock + 手工 unlink 精确制造这个交错，与 gc 独占段内删除等价）：
  - 提交请求先通过 blob 校验，然后在 `Commit` 里等共享锁（`blob 存在？yes`）；
  - 在等待期间删除该 blob，释放锁后提交返回 **`201 {"seq":1}`**；
  - `GET /v1/snapshots/latest` 返回的快照引用了该 hash，但 `GET /v1/blobs/{hash}` 与 `HEAD` 都是 **404**，且快照不可变、blob 不可恢复。
  - 进程内清理（定时器 / HTTP 接口）不会命中该窗口，因为提交全程持 `Guard` 读锁；这条缺口只对外部 `gc` 子命令成立。
- 修复建议：把"blob 存在性 + 大小校验"移进 `Commit`（在共享 `flock` 之后、写临时文件之前，用回调把校验注入），或让 `createSnapshot` 在读取/校验 manifest 之前就取共享 `flock` 并一直持有到 `Commit` 结束；另外让 `gc` 子命令在服务在线时拒绝运行（服务启动时持有一把进程级共享锁/pidfile，子命令用非阻塞独占锁探测），并把"子命令仅限停服"写进帮助文本。

### [P3][F4] 任意一个损坏快照让全数据目录的回收失败，HTTP 只返回 `internal`，且没有面向运维的自愈路径

- 位置：[core/snapshots.go:371-378](../../core/snapshots.go#L371-L378)（`EachManifest` 遇到 `damaged` 直接返回错误）、[app/gc.go:53-60](../../app/gc.go#L53-L60)（错误向上返回，一个 blob 都不删）、[api/admin.go:102-123](../../api/admin.go#L102-L123)（`collectGarbage` 把任何错误映射成 500 `internal`）、[main.go:109-125](../../main.go#L109-L125)（定时器只打 Error 日志）、[core/snapshots.go:345-359](../../core/snapshots.go#L345-L359)（`prune` 只在该 namespace 又有 10 个新版本后才会删掉损坏文件）。
- 触发条件：任意 namespace 里有一个文件首行不是合法 JSON、seq 与文件名不符或内容截断（磁盘错误、误改、备份恢复不完整）。这与上轮 F2 的隔离策略配套——损坏本身已被"跳过 + 头部损坏报错"处理，但回收被整体拒绝。
- 实际结果（`scripts/probe-damaged-gc.sh`，18099）：
  - 把 `work/00000002.jsonl` 写成 `garbage` 后，`list`/`metadata`/`GET /v1/snapshots/3` 都是 200；
  - `POST /admin/api/gc` → **500 `{"error":"internal"}`**，响应里没有任何线索；服务日志只有一条 `ERROR collect garbage`；
  - `gc` 子命令 exit 1 并给出具体文件（`namespace "work" has damaged snapshot 00000002.jsonl`）——说明信息是存在的，只是 HTTP 路径丢弃了它；
  - 再提交一个版本（seq 4）后仍然 500；
  - 另在 18094 上验证：继续提交到 seq 13 后 `prune` 删掉了 00000002，回收恢复 200。也就是说在"该 namespace 再提交 10 个版本"之前，**所有 namespace 的 blob 都不会被回收**，磁盘只增不减，而唯一提示是日志。
- 修复建议：即使维持"有损坏就拒绝回收"的安全策略，也要：① HTTP 响应带可操作的错误码与文件名（如 `409 damaged_snapshot` + `{"file":"work/00000002.jsonl"}`），而不是 `internal`；② 在 `/readyz` 或 `/admin/api/namespaces` 里暴露损坏文件，让面板能看见；③ 提供明确的运维开关（如 `GC_ALLOW_DAMAGED=1` 或 `gc -allow-damaged`）在知情前提下回收其它 namespace 的 blob，并在文档里写清手工删除损坏文件的步骤。

### [P3][F5] `GC_INTERVAL=0` 与"手动触发仍需 GC_TOKEN"这两个开关叠加后，文档承诺的"只保留手动触发"在默认配置下并不存在

- 位置：[README.md:62](../../README.md#L62)（"设成 0 就只保留手动触发"）、[DESIGN.md:265](../../DESIGN.md#L265)（"0 表示不自动回收，只响应 `POST /admin/api/gc`"）、[core/config.go:72-76](../../core/config.go#L72-L76)（允许 0 → 不启动定时器）、[core/config.go:94-101](../../core/config.go#L94-L101)（`GC_TOKEN` 可留空）、[api/router.go:77-80](../../api/router.go#L77-L80)（为空时不注册路由）。
- 事实：关闭自动回收只需要 `GC_INTERVAL=0`，但"手动触发"还额外要求 `GC_TOKEN` 非空。`GC_TOKEN` 的默认值是空（[DESIGN.md:266](../../DESIGN.md#L266)），所以"设成 0 就只保留手动触发"在默认部署里是错的：回收只剩 `gc` 子命令这一条路，服务在线期间没有任何回收手段，blob 会静默堆积。
- 触发条件：运维想"关掉定时回收、需要时手动跑"，于是只设置 `GC_INTERVAL=0`。
- 实际结果（实测，18095，未设置 `GC_TOKEN`）：`POST /admin/api/gc` 与 `GET /admin/api/gc` 都是 **404**（路由未注册），日志只有一条 `gc api disabled`；`GC_INTERVAL=0` 时不启动定时器。两个开关都不报错，没有任何 WARN 说明"当前没有任何回收路径"。
- 修复建议：把文档改成"0 表示不自动回收；手动触发需要同时设置 `GC_TOKEN`，否则只能用 `gc` 子命令（需停服）"；或者在启动时对 `GC_INTERVAL=0 && !GCEnabled()` 打一条 WARN（甚至拒绝启动），避免"以为还有手动回收"的静默失效。

## 待确认（未达到 finding 标准）

1. **回收引用集的内存上界**：[app/gc.go:49-60](../../app/gc.go#L49-L60) 把"所有 namespace × 最多 10 个版本 × 每个最多 10 万条"的 hash 全部放进一个 `map[string]bool`。两个 namespace 满配约 200 万条 key，按每条 64 字节字符串 + map 开销估算在 200–300 MB 量级，超过 `GOMEMLIMIT=200MiB` 与 k8s 的 `limits.memory: 512Mi`。这是既有实现，本轮未做满配压测，无法给出实测数字。
2. **`prune` 的 unlink 没有 fsync 目录**：[core/snapshots.go:345-359](../../core/snapshots.go#L345-L359) 只 `os.Remove`，没有像 `Commit` 那样 `syncDir`。崩溃后可能"复活"一个已被裁剪的旧版本文件；快照不可变，因此不会损坏数据，但会出现"已消失的 seq 又回来了"。是否符合"落盘加固"的目标，需要产品决策。
3. **`syncDir` 失败时数据已经落地**：[core/snapshots.go:316-322](../../core/snapshots.go#L316-L322) 与 [core/blob.go:84-90](../../core/blob.go#L84-L90) 在 rename 成功后才 `syncDir`，失败会把"已提交"报成 500；客户端拿旧 parent 重试会得到 409。只有在文件系统不支持目录 fsync 时才会遇到，本轮未构造。
4. **非 unix 构建静默退化成空锁**：[core/lock_other.go](../../core/lock_other.go) 的 `LockFile` 只打开文件不加锁，提交与回收的互斥在 windows/darwin 上消失，但代码仍能编译、测试仍能通过。当前只发布 linux 产物，是否要在非 unix 上直接 panic/拒绝启动，需要确认。
5. **`adminSnapshot` 不支持 `latest`**：[api/admin.go:76-88](../../api/admin.go#L76-L88) 拒绝非数字 seq，而 `/v1/snapshots/latest` 支持（[api/v1.go:258-263](../../api/v1.go#L258-L263)）。面板只用数字 seq，当前无影响，属接口不对称。
6. **`readyz` 的探针临时文件无人清理**：[core/snapshots.go:412-427](../../core/snapshots.go#L412-L427) 在 `namespaces/` 根下建 `.probe-*` 并删除，但 `Collect` 只清理 `tmp/`。probe 期间崩溃会永久留下该文件（`EachManifest` 会跳过非目录，不影响语义，只是垃圾）。
7. **同一 `DATA_DIR` 上的两个服务进程**：`Commit` 取的是**共享** flock，两个进程可以同时算出同一个 `seq` 并互相覆盖（后 rename 的赢），没有任何 fail-fast。当前部署模型是单实例，是否需要显式拒绝（例如启动时独占一把锁）值得确认。
8. **2 个并发登录的确定性（竞态窗口）**：本轮 18099 上 2 并发第 3 轮双双 503，18094 上 5 轮里 3 轮复现，18097 上 4 轮里 1 轮复现，18096 上 4 轮未复现——竞态窗口在"外层取槽位到内层取槽位"之间（约几百微秒）。5 个并发是稳定复现的。修复后建议用 5 并发做回归基线。

## 未验证项

- **断电/崩溃注入**：`fsync` 与 `syncDir` 只有代码路径证据，没有 strace 或掉电注入；也没有验证"rename 后目录 fsync 失败"的真实行为。
- **`sendfile` 是否真的生效**：`recorder.ReadFrom` 只是把 `io.ReaderFrom` 转发给 `*http.response`，没有 syscall 级观察（本机无 strace）。
- **413 路径**：`>64 MiB` 的提交请求体、对已存在 blob 的 257 MiB `PUT` 本轮没有重跑（上轮记录为 413）。
- **压测数字**：本轮只观察到 90 000 条 manifest 的提交往返约 0.5 s、快照文件 11 880 053 字节；README 的"提交 10 万条峰值 85 MB""8 并发读 59 MB"没有重新测量，GC 引用集的内存上界也只是估算（见待确认 1）。
- **浏览器交互**：只截了 `/auth` 与 `/admin?ns=default&seq=14` 两张图，登录表单提交、语言切换、树节点点击、blob 下载没有逐项驱动。
- **Windows/macOS 运行时**、arm64 产物运行（只做了交叉编译与 `file`/`ldd` 检查）。
- **真实并发运行的 `gc` 子命令**：F3 的探针用外部 `flock` + 手工 `unlink` 精确制造交错，语义等价，但没有去采样子命令自身的删除时序。

## 附：验证脚本

| 脚本 | 用途 |
| --- | --- |
| `scripts/run-all.sh [PORT]` | 用全新 `DATA_DIR` 起一个独立实例（默认 18096），依次跑下面四个探针，退出时 kill |
| `scripts/probe-login.sh BASE` | F1：单次/2 并发×4 轮/5 并发登录，打印状态码与耗时 |
| `scripts/probe-guard-stall.sh BASE DATA_DIR` | F2：造 90 000 条快照，慢速读（20 KB/s）与慢速请求体两种方式卡住读锁，观察 GC 与 `/v1/metadata` 挂起 |
| `scripts/probe-flock-gap.sh BASE DATA_DIR` | F3：外部独占 flock + 删除 blob，复现引用悬空 blob 的快照 |
| `scripts/probe-damaged-gc.sh BASE DATA_DIR` | F4：损坏中间版本，观察 HTTP 500 / 子命令 exit 1 / 再提交后仍失败 |

脚本依赖：`curl`、`python3`、`bash`；二进制默认 `/tmp/sc-review-c7f31d/app`（不存在时用 `go build` 现场编译），配置默认 `/tmp/sync-test/config.yaml`。

## 附：环境观察（与本轮评审结论无关，供 lead 核对）

评审开始时（20:42）`http://127.0.0.1:18080/livez` 返回 200；约 20:57 复查时 18080 已无监听、系统中也没有 `sync-center` 进程。本轮所有 `kill` 只针对自建实例的 18094–18099（脚本按端口反查 pid），未对 18080 执行任何操作；时间上与本次会话被 DSH 重启的时点重合。若主服务需要保持在线，请 lead 重新拉起并确认。
