# 代码审查记录

| 项 | 值 |
| --- | --- |
| 审查对象 | `9c52bc2b07ab25c1cc54763e0175484a82226d1c`（短 SHA `9c52bc2`） |
| 分支 | `main` |
| 审查时间 | 2026-11-12T19:51:05+0800（Asia/Shanghai，UTC+8） |
| 审查目录 | `code-reviews/2026-11-12T19-51-05+0800_9c52bc/` |
| 开始审查时工作区 | 干净（`git status --porcelain` 无输出） |
| 审查结束时工作区 | 仅新增未跟踪目录 `code-reviews/`；生产代码与测试代码未改动，未提交 |
| 上一轮审查 | `code-reviews/` 此前不存在，这是本项目的首轮记录；因此第一条只做了“重点提交 diff 复核” |

## 审查范围

仓库全部内容（Go 1.27，单静态二进制，`CGO_ENABLED=0`，Vue 3 + Vite 产物经 `go:embed` 打包）：

- 文档基准：`DESIGN.md`（14 节）、`README.md`；
- Go 代码：`main.go`、`web.go`、`api/{router,auth,ui,v1}.go`、`app/{app,lease,gc}.go`、`core/{config,manifest,snapshots,blob,token,session}.go`、`httpx/{httpx,clientip}.go`、`i18n/i18n.go`；
- 测试代码：`core/{config_test,snapshots_test}.go`（只读，未修改）；
- 前端：`frontend/src/**`、`vite.config.ts`；
- 构建与部署：`justfile`、`Dockerfile`、`.dockerignore`、`k8s.sync-center.yaml`、`.github/workflows/`；
- 变更历史：`git log --oneline`（11 个提交）与 `git show` 完整 diff，重点是 `9c52bc2`（SQLite → 每 namespace 目录下的 JSONL，删除 `db` 包）、`c39ce70`（`LEASE_TTL`）、`1f5f286`（token/namespace 字符集收紧），并对比了 `../sync-center` 的 Python 原型（布局与 gc 实现）。

重点提交复核结论：`c39ce70` 与 `1f5f286` 的改动与 `DESIGN.md` 第 5、10 节一致，没有发现问题；`9c52bc2` 的存储迁移在语义上基本等价（parent 校验、相同 manifest 不建版本、保留 10 个版本、失败不落盘），但引入了下文 F2/F3/F6 以及文档漂移 F4/F15。

## 实际执行的构建与测试

| 命令 | 结果 |
| --- | --- |
| `go version` | `go1.27.3 linux/amd64` |
| `go build ./...` | 通过（exit 0，无输出） |
| `go vet ./...` | 通过（exit 0，无输出） |
| `go test ./... -count=1 -v` | 通过：`sync-center/core` ok 0.007s（14 个用例全部 PASS）；`api`/`app`/`httpx`/`i18n`/`main` 无测试文件 |
| `cd frontend && pnpm typecheck` | 通过（`vue-tsc -b`，exit 0） |
| `bash scripts/probe-api.sh` | 见下文 F1/F2/F4/F9/F10/F11 的证据 |
| `bash scripts/probe-concurrency.sh` | 见下文 F1/F7 的证据 |
| `bash scripts/probe-config.sh` | 见下文 F11 的证据 |

HTTP 探测使用独立 `DATA_DIR`（`mktemp -d`）与独立端口（18181/18182/18183/18184），未占用 18080，未触碰仓库内 `.data`。所有探针实例在脚本退出时被 kill。

`frontend/dist` 是 `go:embed` 的必需输入，但已在 `.gitignore` 中忽略；本次审查时工作区已有构建产物，因此 `go build ./...` 可直接通过。全新 clone 上需要先 `just frontend`（`just vet`/`just test` 有 `frontend-stub` 兜底），这一点未计入 findings。

## Findings

### [P1][F1] 未认证的登录风暴既能阻塞全部 `/v1`，又能把内存推到 OOM

- 位置：[api/router.go:34-35](../../api/router.go#L34-L35)、[api/router.go:55](../../api/router.go#L55)、[api/router.go:172-178](../../api/router.go#L172-L178)、[api/auth.go:20-60](../../api/auth.go#L20-L60)、[core/session.go:19](../../core/session.go#L19)、[k8s.sync-center.yaml:86-90](../../k8s.sync-center.yaml#L86-L90)
- 事实：`/api/auth` 与 `/v1`、`/ui/api` 共用同一个 `apiGate`（容量 `MAX_API_CONCURRENCY`，默认 128）；登录路径每次都跑完整 Argon2id（`argonMemory = 19*1024` KiB），并且为了消除时间侧信道，无论成功失败都睡满 1-1.2 秒，期间一直占着 gate 槽位。`gate` 用阻塞发送 `sem <- struct{}{}` 拿配额，没有超时也没有 `r.Context()` 参与。
- 触发条件：任何未认证客户端向 `POST /api/auth` 并发提交若干请求（body 只需 8 KB 以内的 `{"token":"x"}`）。
- 实际结果（实测，`scripts/probe-concurrency.sh`，`MAX_API_CONCURRENCY=8`）：
  - 空载 `VmHWM=30260 KB`；8 个并发登录瞬间升到 `168772 KB`（+138 MB ≈ 8 × 17-19 MiB）；
  - 登录期间一次正常的 `GET /v1/metadata` 耗时 **0.7645 s**（空载 0.0005 s），即被登录流量堵住；
  - 按默认 128 并发外推，峰值约 `128 × 19 MiB ≈ 2.4 GiB`；仓库自带的 k8s 清单 `limits.memory: 512Mi`，三十来个并发登录即可越过限额触发 OOMKill 并进入重启循环；`GOMEMLIMIT` 对 Argon2 的堆外式分配无效。
- 修复建议（按直接程度排序）：给登录单独一个容量很小（如 2）的 semaphore，且不要让 1 秒的等待占住任何 `apiGate` 槽位（先释放再 sleep）；如在反向代理之外还要兜底，再叠加按 IP 的轻量令牌桶。关键是“登录并发”与“API 并发”必须是两个互相独立的预算。

### [P2][F2] 单个损坏的快照文件让整个 namespace 永久 500，且没有隔离或自愈路径

- 位置：[core/snapshots.go:86-101](../../core/snapshots.go#L86-L101)（`List` 遇到坏文件直接返回错误）、[core/snapshots.go:205-207](../../core/snapshots.go#L205-L207)（`Commit` 依赖 `List`）、[core/snapshots.go:305-319](../../core/snapshots.go#L305-L319)（`prune` 只删低 seq，永远碰不到 head）、[app/gc.go:12-24](../../app/gc.go#L12-L24)
- 触发条件：`namespaces/<ns>/<%08d>.jsonl` 首行缺失/不是合法 JSON/seq 与文件名不符（崩溃截断、磁盘错误、手工误改、备份恢复不完整）。
- 实际结果（实测，`scripts/probe-api.sh` 第 7 节：把 work 的 head 文件截断为 0 字节）：
  - `GET /v1/snapshots?limit=5` → 500、`GET /v1/snapshots/latest` → 500、`GET /v1/metadata` → 500；
  - `POST /v1/snapshots` → 500（提交彻底不可用，客户端无法靠重试自愈）；
  - `GET /ui/api/namespaces` 与 `/ui/api/namespaces/work/snapshots` → 500（面板整页报错）；
  - 旧快照 `GET /v1/snapshots/11` 仍然 200，说明数据本身在，只是被一个坏文件挡死；
  - `go` 二进制 `gc` 子命令同样直接失败（`{"msg":"collect garbage","error":"00000012.jsonl: empty snapshot"}`），连 blob 回收也被一个坏文件拖死。
- 修复建议：`List`/`EachManifest` 对单个文件的解析失败要“记录并跳过”（或重命名为 `.corrupt` 并继续），head 取可用文件的最大 seq；`readyz` 或启动时把可检测的损坏暴露到日志/探针里。只有“跳过”才能让 namespace 在部分损坏时仍然可读、可提交；如果产品上更希望 fail-fast，至少要给出明确的操作提示与自愈命令，而不是统一 500。

### [P2][F3] 快照与 blob 落盘都没有 fsync，崩溃后可能留下截断文件（正是 F2 的触发源）

- 位置：[core/snapshots.go:222-282](../../core/snapshots.go#L222-L282)（`CreateTemp` → `Write` → `Close` → `Rename`，无 `Sync`）、[core/blob.go:55-85](../../core/blob.go#L55-L85)（同样的模式）
- 触发条件：提交/上传过程中进程被 kill、机器断电或内核崩溃；`Rename` 的元数据可能先于文件数据落盘。
- 实际结果：rename 之后文件名存在但内容为空或不完整，于是得到 F2 那种“head 文件存在但首行缺失”的状态；服务和文档都假设“临时文件才是崩溃残留”（`DESIGN.md` 第 3、4 节、`README.md` 第 66 行），没有覆盖“正式文件本身被截断”的情况。
- 修复建议：`tmp.Sync()` 之后再 `Close`/`Rename`，rename 后再对 `namespaces/<ns>` 目录做一次 `Sync`（blob 同理，同步 `blobs/xx/yy`）。如果明确接受“断电可能损坏最后一个快照”，就把这个取舍写进 DESIGN 的持久性一节，并配合 F2 的隔离逻辑。

### [P2][F4] README 承诺“与 Python 原型共用数据目录”，新的 JSONL 布局已让该承诺失效，混用会被 gc 删掉对侧仍在引用的 blob

- 位置：[README.md:5](../../README.md#L5)、[core/snapshots.go:51-56](../../core/snapshots.go#L51-L56)、[DESIGN.md:44-52](../../DESIGN.md#L44-L52)、[app/gc.go:12-24](../../app/gc.go#L12-L24)
- 事实：`9c52bc2` 之前两侧都用 `data/sync.db`（同一套 SQLite schema），所以“共用数据目录”成立；本提交把 Go 侧改成 `namespaces/<ns>/*.jsonl` 并删掉了 `db` 包，而 Python 原型仍是 `data/sync.db`（`../sync-center/app/main.py:29`、`../sync-center/DESIGN.md:44-48`），README 的这句话没有跟着改。
- 触发条件：按 README 第 5 行把两个实现指向同一个 `DATA_DIR`。
- 实际结果：两侧互相看不到对方的快照（Go 的 `List` 只读 `namespaces/`，Python 只读 `sync.db`），同一个 namespace 出现两个互相不知情的 head，客户端会反复 409/覆盖；更糟的是 `gc` 只用 Go 自己的快照汇总引用集，会把 Python 侧快照仍在引用的、超过 1 小时的 blob 删掉，导致 Python 侧快照永久缺 blob（不可恢复）。
- 修复建议：改 README，明确两个实现的存储布局已经不同、不能共用数据目录；如果确实需要共用，`gc` 至少要检测 `sync.db` 存在就拒绝运行（fail fast），而不是默默删 blob。

### [P2][F5] gc 删除前不复核引用，且判据是 blob 的 mtime 而不是提交时间，与并发提交竞争会删掉刚被引用的 blob

- 位置：[app/gc.go:12-24](../../app/gc.go#L12-L24)、[core/blob.go:88-109](../../core/blob.go#L88-L109)、对照 [DESIGN.md:206](../../DESIGN.md#L206)
- 事实：`CollectGarbage` 先扫全部 manifest 得到 `referenced` 快照集，再交给 `BlobStore.Collect` 遍历 `blobs/` 删除；`Collect` 只按“`referenced[entry.Name()]` 不含 + mtime 早于 now-1h”删除，删除前**没有**再确认一次引用。DESIGN 第 8 节明确要求“删除前重新确认无引用，并与提交操作保持足够的时间间隔”。
- 触发条件：客户端在 T-2h 上传过 blob 但提交失败（或被 409 打断），T 时刻重试时按客户端约定先 `HEAD` 发现已存在、跳过上传，随后提交；与此同时外部调度的 `gc` 正在这两步之间运行。
- 实际结果：新快照引用的 blob 在 gc 的引用集里不存在，且 mtime 早于 cutoff，于是被删除；快照写成功后立刻变成“引用了不存在的 blob”，之后对该文件的 pull 永久 404，服务端没有任何修复手段（快照不可变）。
- 修复建议：`Collect` 每删一个候选前用一次“廉价复核”——至少对候选重扫一遍引用集（或对单个 hash 检查是否出现在最近 N 个快照里）；更稳妥的是把“判定 + 删除”限制在提交锁内，或按“快照提交时间”而不是 blob mtime 计算 grace。当前实现里 `SnapshotStore.mu` 已经存在，但没有和 gc 共享。

### [P3][F6] 版本裁剪与并发读之间没有同步，读请求可能撞上刚被删除的文件并返回 500

- 位置：[core/snapshots.go:305-319](../../core/snapshots.go#L305-L319)（`prune` 无锁）、[core/snapshots.go:283](../../core/snapshots.go#L283)（只在 `Commit` 里持 `mu`）、[core/snapshots.go:79-99](../../core/snapshots.go#L79-L99)（`List`/`readMeta` 不持锁）
- 触发条件：一个 `GET /v1/snapshots`（或 `/v1/metadata`、`/ui/api/namespaces`）已经 `ReadDir` 拿到文件名列表，另一个提交刚好完成第 11 个版本并 `prune` 掉最老的一个；读者随后 `os.Open` 那个已被删除的文件。
- 实际结果：`readMeta` 返回 `ENOENT`，`List` 把整个请求变成 500（而不是忽略这个刚消失的版本）。窗口很窄，但一旦命中，客户端会看到“偶发 500”，且没有任何重试。
- 修复建议：`List`/`Info`/`prune` 共用一把 `RWMutex`（读者持读锁，`prune` 持写锁），或让 `List` 对 `ENOENT` 跳过该条；后者更简单，也更符合“目录里存在什么就报什么”的语义。

### [P3][F7] gate 阻塞获取 + 服务端没有 Read/Write 超时：未认证的半开连接可以长期占满配额

- 位置：[api/router.go:172-178](../../api/router.go#L172-L178)、[main.go:52-56](../../main.go#L52-L56)
- 事实：`gate` 是 `sem <- struct{}{}`，既不响应 `r.Context()` 也不超时；`http.Server` 只设了 `ReadHeaderTimeout`，没有 `ReadTimeout`/`WriteTimeout`/`IdleTimeout`，因此“发完请求头、body 只发 1 字节”的连接可以无限期占住一个槽位。
- 实际结果（实测，`scripts/probe-concurrency.sh` 第二阶段，`MAX_API_CONCURRENCY=8`）：8 条从未认证的 `POST /api/auth` 半开连接（`Content-Length: 8192`，只发 1 字节）建立后，一次正常的 `GET /v1/metadata` 在 3 秒内拿不到响应，直到探针在 3.5 秒主动关闭这些连接时才返回 200（耗时 2.9 s）。把探针里的 `sleep` 换成永久保持，就得到一个不需要任何凭据的长期拒绝服务。
- 修复建议：`gate` 改成 `select` + `r.Context().Done()` + 排队超时（超时返回 503/429，而不是无限等待）；同时给 `http.Server` 补 `ReadTimeout`/`WriteTimeout`/`IdleTimeout`（大文件传输用 `http.ResponseController` 单独放宽）。

### [P3][F8] blob 已存在时无上限读取并丢弃整个请求体，256 MiB 限制与超时都不生效

- 位置：[api/v1.go:84-88](../../api/v1.go#L84-L88)
- 触发条件：`PUT /v1/blobs/{hash}` 的 hash 已在库里，客户端仍发送很大的 body（或 chunked 无限流）。
- 实际结果：这条分支没有 `MaxBytesReader`、没有 `LimitReader`、没有大小判断，`io.Copy(io.Discard, r.Body)` 会一直读到 EOF；DESIGN 第 6 节“单文件上限默认 256 MiB，超限返回 413”在这条路径上不成立，服务端带宽与连接被无上限占用（并与 F7 叠加：始终占住一个 gate 槽）。
- 修复建议：这条分支同样套 `http.MaxBytesReader` + `io.CopyN(io.Discard, r.Body, MaxBlobSize+1)`，超过上限返回 413（或直接 `http.ResponseController` 设读超时后断开）；既然已经存在的 blob 不需要内容，也可以先回 204 再异步短读排空，并限制排空字节数。

### [P3][F9] `HEAD /v1/blobs/{hash}` 设置的 Content-Length 在 204 响应中被丢弃

- 位置：[api/v1.go:119-120](../../api/v1.go#L119-L120)
- 事实：`w.Header().Set("Content-Length", ...)` 之后写 204；按 RFC 7230，204 响应不能带 Content-Length，Go 的 `net/http` 会把它删掉。响应头实测只有 CSP 等安全头（`scripts/probe-api.sh` 第 5 节），没有 Content-Length。
- 触发条件/影响：任何想用 HEAD 拿到 blob 大小的客户端都拿不到（Python 原型同样如此，所以现有客户端只判断状态码，未暴露问题）。这属于“写了但永远不会生效”的代码，容易让后续实现者误以为该接口会返回大小。
- 修复建议：要么删掉这行（明确 HEAD 只返回存在性），要么改成 200 + `Content-Length`（同时保持 `HEAD` 语义），要么用自定义头（如 `X-Blob-Size`）。`DESIGN.md` 第 6 节对 HEAD 只承诺 204/404，建议按“删掉 + 在文档里写明 HEAD 不返回大小”处理。

### [P3][F10] `gc` 子命令因先加载完整配置而强制要求面板密钥，README 给的命令必然失败

- 位置：[main.go:23-49](../../main.go#L23-L49)、[README.md:62-64](../../README.md#L62-L64)
- 触发条件：按 README 执行 `DATA_DIR=$PWD/.data ./.dist/app-linux-amd64 gc`。
- 实际结果（实测）：进程直接退出，`{"level":"ERROR","msg":"load config","error":"AUTH_TOKEN must be 16-128 characters long"}`；补上 `AUTH_TOKEN` 与 `SESSION_SIGNING_KEY` 之后才会输出 `{"msg":"garbage collected","removed":0}`。也就是说“跑一次垃圾回收”必须提供与回收无关的面板口令和会话密钥。
- 修复建议：先解析 `os.Args`，`gc` 走一条只需要 `DATA_DIR`（以及可选 `CONFIG_FILE`）的加载路径；或者把 `AUTH_TOKEN`/`SESSION_SIGNING_KEY` 的校验移到服务端启动分支。

### [P3][F11] 同一 namespace 内重复配置同一个 token 时静默后者覆盖前者，权限被悄悄改变

- 位置：[core/token.go:18-34](../../core/token.go#L18-L34)
- 事实：`NewTokenIndex` 用 `index[digest] = Principal{...}` 直接覆盖，同一 namespace 内重复的 token 不报错；跨 namespace 的重复反而会被拒绝。
- 实际结果（实测，`scripts/probe-config.sh`）：配置 `["TOKENRW_0123456789:rw", "TOKENRW_0123456789:r"]` 时服务正常启动，`/v1/metadata` 返回 `"mode":"r"`、`POST /v1/lease` 返回 403——第一行的 `rw` 被静默丢弃；把两行顺序对调则得到 `rw`。运维在轮换 token 时复制粘贴出错，就会得到“能启动、但权限和预期不同”的状态，违反“启动时校验，不合法直接拒绝启动”的设计约定。
- 修复建议：在同一 namespace 内检测重复的 token 摘要：权限不同直接报错（`token %q declared twice with different permissions`），权限相同也建议报错或去重并打印 warning。

### [P3][F12] 服务端 i18n 里的 15 条 `ui.*` 文案已无调用者，与前端词典重复且已漂移

- 位置：[i18n/i18n.go:42-66](../../i18n/i18n.go#L42-L66)、[frontend/src/i18n.ts:5-30](../../frontend/src/i18n.ts#L5-L30)
- 事实：面板改成 Vue 之后，服务端只输出 `error.*` 文案；`grep '"ui\.' --include=*.go` 在 `i18n/i18n.go` 之外没有任何命中，这 15 条（含 `ui.namespaces`、`ui.updated-at`、`ui.download` 等）是死数据。两份词典已经漂移：服务端用 `"%s"/"%d"` 占位，前端用 `"{time}"/"{size}"`；前端多一条 `ui.language`。
- 实际结果：同一批界面文案有两个事实来源，改一处不会同步另一处；初次阅读者会误以为服务端也会渲染页面文案。
- 修复建议：删掉服务端 `ui.*` 这一块，服务端只保留 `error.*`，前端词典作为唯一来源（并在 DESIGN 第 6 节写明这一点）。

### [P3][F13] 死代码：`core.Parse` 与 `errMissingSecret`

- 位置：[core/manifest.go:79-84](../../core/manifest.go#L79-L84)、[core/session.go:116](../../core/session.go#L116)
- 事实：`core.Parse` 注释写着“供 WebUI 与校验使用”，但全仓（含测试）已无调用者——`9c52bc2` 把 gc 改成 `EachManifest` 流式读取之后就没人用了；`errMissingSecret` 只有定义，`core/session.go` 里只有它用到 `errors` 包。
- 实际结果：编译不报错，但保留了两处会误导后续维护者的历史遗留（读代码时会以为还有一条“解析存储 manifest”的路径）。
- 修复建议：删除二者（`session.go` 同时去掉 `errors` 导入）。

### [P3][F14] 日志中间件的 ResponseWriter 包装使 sendfile/Flush 失效，与 README 的 sendfile 说法不符

- 位置：[api/router.go:211-248](../../api/router.go#L211-L248)（`recorder` 只实现了 `WriteHeader`/`Write`）、[README.md:84](../../README.md#L84)
- 事实：`log` 对除 `/livez` 外的所有请求都套 `*recorder`；`http.ServeFile`/`io.Copy` 只有在目标实现 `io.ReaderFrom`（`*http.response` 会进而走 `sendfile`）时才零拷贝，包装类型没有该接口，也没有 `Flush`/`Unwrap`，于是 50 MiB 下载退化成 32 KiB 用户态缓冲区循环。
- 实际结果：功能正确，但 README 第 84 行“流式写盘 + `sendfile`”的说法在实现里不成立（`GOMEMLIMIT`/内存数字仍然是安全的，只是吞吐达不到 sendfile 的水位）。
- 修复建议：`recorder` 增加 `Unwrap() http.ResponseWriter`，并在底层实现了 `io.ReaderFrom`/`http.Flusher` 时转发 `ReadFrom`/`Flush`；或者对 `GET /v1/blobs`、`/ui/api/blob` 不做状态码日志包装（用轻量的状态钩子）。

### [P3][F15] 文档失效：README 的测试 token 示例已不合法，DESIGN 引用了仓库里不存在的图

- 位置：[README.md:54](../../README.md#L54)、[README.md:44](../../README.md#L44)、[DESIGN.md:20](../../DESIGN.md#L20)
- 事实一：README 第 54 行让读者在配置里放 `TOKENRW:rw`、`TOKENR:r`，而第 44 行（`1f5f286` 加入）要求 token 主体 16-128 位；实测用该配置启动直接退出：`namespace "default": token must be 16-128 characters of letters, digits and underscores`。
- 事实二：`DESIGN.md:20` 的 `![数据模型](model.svg)` 指向的文件不在本仓库（`git ls-files` 无 `model.svg`，它只存在于 Python 原型），渲染出来是坏图。
- 修复建议：把 README 的示例换成合法长度的 token（例如 `SYNC_DEFAULT_RW_2026:rw`），并同步 `tests/smoke.py` 的配置说明；给 DESIGN 补上 `model.svg` 或删掉该行。

### [P3][F16] 前端面板：`?seq=` 非数字会变成 NaN 请求，预览失败时把错误 JSON 当文件内容显示

- 位置：[frontend/src/components/Panel.vue:109-110](../../frontend/src/components/Panel.vue#L109-L110)、[frontend/src/components/Panel.vue:96-101](../../frontend/src/components/Panel.vue#L96-L101)
- 触发条件一：访问 `/ui?seq=abc`（或任何非数字）→ `Number("abc")` 为 `NaN` → 请求 `/ui/api/namespaces/<ns>/snapshots/NaN` → 404 → 面板显示“加载失败”，且 `activeSeq` 停在 `NaN`。
- 触发条件二：预览一个已被删除的 blob（`/ui/api/blob/<hash>` 返回 404）→ `fetchOrLogin` 只处理 401，随后 `response.text()` 把 `{"error":"blob_not_found",...}` 原样显示成“文件内容”。
- 修复建议：`const value = Number(seq); if (Number.isInteger(value) && value > 0) await selectSnapshot(value);`；预览前判断 `response.ok`，失败时给出本地化提示而不是把错误体当正文。

## 待确认（未达到 finding 标准）

1. `maxSnapshotBody = 64 MiB`（[api/v1.go:16](../../api/v1.go#L16)）与 `DESIGN.md` 第 5 节“单个 manifest 上限 10 万条”的关系：路径接近 1024 字节上限时，10 万条无法装入 64 MiB，超限返回的是 422 `invalid_body` 而不是 413；两个上限哪个是权威、超限该回什么码，文档没有写清楚，本次未构造 64 MiB 请求实测。
2. 提交路径会把整份 manifest 读进内存（`json.RawMessage` + `[]Entry`，`Normalize` 后还要排序），`DESIGN.md` 第 3 节“服务端不必把 manifest 读进内存”只对读取/比对成立；在 `MAX_API_CONCURRENCY=128` 下理论峰值是 128 × 64 MiB。README 已给出 165 MB 的实测，是否接受这个上界需要产品决策，本次未压测。
3. `NamespaceInfo.CreatedAt`（[core/snapshots.go:112-131](../../core/snapshots.go#L112-L131)）从“namespace 创建时间”变成了“现存最老快照的时间”，裁剪后会向前跳变；空 namespace 用目录 mtime 兜底。UI 只显示 `updatedAt`，因此当前没有可见影响，但接口字段语义已经变了。
4. `identical` 判定是“逐行字节比对”（[core/snapshots.go:250-278](../../core/snapshots.go#L250-L278)），依赖存储行的序列化细节；如果将来有第二个实现写同一个目录（F4 之后已不成立），字节差异会被误判成“内容不同”而多建一个版本。当前单实现下没有问题。
5. 每次 `Commit` 都会 `List` 整个 namespace 目录并逐个打开文件读首行（[core/snapshots.go:205](../../core/snapshots.go#L205)）；保留 10 个版本时开销可忽略，但若以后把保留数调大，提交延迟会随之线性上升。
6. `app.New` 会对 `DATA_DIR` 执行 `os.Chmod(0o700)`（[app/app.go:30](../../app/app.go#L30)），会改动运维指定的目录权限；在只读挂载或非属主目录上直接启动失败。是否是有意为之，未见文档说明。

## 未验证项

- `pnpm build` 与浏览器端交互（登录跳转、语言切换、树渲染、下载）：只跑了 `pnpm typecheck`，本机未开浏览器。
- `docker build`、`just build` 的交叉编译产物（amd64/arm64）与 `ldd` 静态性：未执行，只跑了 `go build ./...`。
- 大规模压测：10 万条 manifest、256 MiB blob、`MAX_API_CONCURRENCY=128` 下的真实内存与并发行为，均未复现。
- F5（gc 与提交竞争删 blob）与 F6（`prune` 与读竞争）没有实际复现：前者需要精确卡在两阶段之间，后者窗口在微秒级；两条结论均来自代码路径分析。F3（无 fsync）同样只能靠代码论证，未做断电注入。
- Windows 平台与 arm64 运行时行为未验证；`http.ServeFile` 的 Range/缓存语义只做了存在性检查。

## 附：验证脚本

| 脚本 | 用途 |
| --- | --- |
| `scripts/probe-api.sh` | 独立实例上验证租约生命周期、409 释放租约、相同 manifest 不建版本、manifest 校验、认证/授权、版本保留、损坏快照的影响、gc 子命令的两种调用方式 |
| `scripts/probe-concurrency.sh` | 登录风暴对 `/v1` 的阻塞与 Argon2 内存放大；未认证半开连接占满 `apiGate` 后 `/v1` 无响应 |
| `scripts/probe-config.sh` | 同一 namespace 内重复 token 时权限被静默覆盖 |

脚本依赖：`curl`、`python3`、`sha256sum`；二进制默认 `/tmp/sc-probe/app`（`go build -o /tmp/sc-probe/app .`），也可用第一个参数指定。
