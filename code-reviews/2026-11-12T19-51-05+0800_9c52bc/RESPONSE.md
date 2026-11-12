# 代码审查响应

| 项 | 值 |
| --- | --- |
| 审查记录 | [REVIEW.md](REVIEW.md) |
| 响应时间 | 2026-11-12T20:12+0800（Asia/Shanghai，UTC+8） |
| 处理结果 | 16 条 finding 全部处理：14 条修复，2 条按新语义修复；6 条待确认逐条回应 |
| 提交 | 与本文件同一次提交，提交信息引用本目录名 |

## 总览

| # | 级别 | 结论 | 一句话 |
| --- | --- | --- | --- |
| F1 | P1 | 修复 | 登录使用独立配额，恒定耗时等待移出槽位 |
| F2 | P2 | 修复（语义调整） | 跳过损坏文件；只有“最新版本损坏”才报错 |
| F3 | P2 | 修复 | 快照与 blob 写入、改名后都 fsync |
| F4 | P2 | 修复 | README 声明布局不同；gc 遇到 `sync.db` 直接拒绝 |
| F5 | P2 | 修复 | gc 持独占锁、提交持共享锁，引用集与删除原子化 |
| F6 | P3 | 修复 | 并发裁剪导致的 ENOENT 视为“已消失”跳过 |
| F7 | P3 | 修复 | 闸门支持 ctx 与排队超时；补 IdleTimeout 与登录读超时 |
| F8 | P3 | 修复 | 已存在 blob 的排空同样受 256 MiB 限制 |
| F9 | P3 | 修复 | 删除 204 响应上永不生效的 Content-Length |
| F10 | P3 | 修复 | gc 只加载数据目录，不再要求面板密钥 |
| F11 | P3 | 修复 | 重复 token 直接拒绝启动 |
| F12 | P3 | 修复 | 删除服务端 15 条无调用的 `ui.*` 文案 |
| F13 | P3 | 修复 | 删除 `core.Parse` 与 `errMissingSecret` |
| F14 | P3 | 修复 | `recorder` 补 Unwrap / ReadFrom / Flush |
| F15 | P3 | 修复 | README token 示例、补齐并更新 `docs/model.svg` |
| F16 | P3 | 修复 | 前端 `?seq=` 守卫、预览检查 `response.ok` |

## 逐条响应

### [P1][F1] 未认证登录风暴阻塞 /v1 并放大内存

- 做法：`/api/auth` 改用独立闸门 `authGate`（容量 2），不再与 `/v1` 共用 `apiGate`；`login` 在 Argon2 校验完成后立即释放槽位，恒定 1 秒等待在槽位外进行；读取登录请求体前设置 15 秒读超时。
- 位置：`api/router.go`（`authGate`、`gate`）、`api/auth.go`（`login`）。
- 验证：重跑 `scripts/probe-concurrency.sh`，8 个并发登录下 `VmHWM` 由 28324 KB 升到 58536 KB（修复前 168772 KB），登录风暴期间 `GET /v1/metadata` 耗时 0.0005 s（修复前 0.7645 s）。

### [P2][F2] 单个损坏文件让 namespace 永久 500

- 做法：`List` 改为返回 `(metas, damaged, error)`，无法解析的文件跳过并记录警告；`latest` 与 `metadata` 用 `DamagedAhead` 判断损坏文件是否比可读 head 更新，是则返回 500，绝不把旧版本当成 latest；列表、面板、提交继续可用，提交会以同 seq 覆盖损坏文件从而修复；`EachManifest` 在存在损坏时返回错误（见 F5 说明）。
- 位置：`core/snapshots.go`（`List`、`DamagedAhead`、`EachManifest`）、`api/v1.go`、`api/ui.go`。
- 验证：`probe-api.sh` 第 7 节：head 截断后列表 200、面板 200、旧快照 200、`latest`/`metadata` 500；另做中间版本损坏测试，列表、`latest`、`metadata` 均为 200。
- 说明：头部损坏时选择报错而不是降级，是为了避免客户端把旧状态当作最新拉回去。删除损坏文件即可回退，提交也会覆盖同 seq 的损坏文件。

### [P2][F3] 落盘无 fsync

- 做法：快照写入后 `tmp.Sync()`，rename 后 `syncDir`；blob 同样处理。新增 `core/fs.go` 的 `syncDir`。
- 位置：`core/snapshots.go`（`Commit`）、`core/blob.go`（`Write`）、`core/fs.go`。
- 验证：提交/上传后文件可读、测试通过；本机没有 strace，未做断电注入，只是代码路径验证。

### [P2][F4] README 的“共用数据目录”已失效

- 做法：README 明确两种实现的存储布局不同、不能指向同一目录；`gc` 在数据目录发现 `sync.db` 时直接拒绝运行，不删除任何 blob。
- 位置：`README.md`、`app/gc.go`。
- 验证：在数据目录放一个空 `sync.db`，`gc` 返回 `found sync.db ... refusing to delete blobs`。

### [P2][F5] gc 与提交竞争可能删掉刚被引用的 blob

- 做法：新增 `core/lock_unix.go` 与 `core/lock_other.go`，基于 `flock`：提交持共享锁，gc 全程持独占锁。扫描引用集与删除 blob 在同一个独占锁内完成，提交在 gc 期间阻塞，引用集不会中途变化。
- 位置：`core/lock_unix.go`、`core/lock_other.go`、`core/snapshots.go`（`LockAll`、`Commit`）、`app/gc.go`。
- 验证：用 `flock -x .lock` 持有 3 秒，期间的提交耗时 2718 ms（等待锁），说明两者互斥。
- 说明：删除判据仍是 blob mtime，没有改成提交时间；在新的互斥关系下，“gc 期间产生新引用”不可能发生。客户端先传 blob、很久以后才提交的场景，提交会因为 blob 不存在而返回 422，不会产生引用缺失的快照。

### [P3][F6] 裁剪与读并发导致偶发 500

- 做法：`List` 把读取时的 `ENOENT` 视为文件已被裁剪，跳过该条而不是整体报错。
- 位置：`core/snapshots.go`（`List`）。
- 验证：代码路径；该窗口在微秒级，未做确定性复现。

### [P3][F7] 闸门阻塞获取、没有读写超时

- 做法：`gate` 改为 `select`，响应 `r.Context().Done()`，排队超过 10 秒返回 503 `busy`；`http.Server` 增加 `IdleTimeout` 120 秒；登录路径单独设置 15 秒读超时。
- 位置：`api/router.go`、`api/auth.go`、`main.go`、`i18n/i18n.go`（`error.busy`）。
- 验证：`probe-concurrency.sh` 第二阶段，8 条未认证半开连接存在时 `GET /v1/metadata` 仍在 0.0005 s 返回（修复前 3 秒超时）。
- 说明：没有设置全局 `ReadTimeout`/`WriteTimeout`，因为 256 MiB blob 在慢速链路上可能持续数分钟，全局值会误杀正常传输；改为按用途设读超时，并在文档里写明取舍。

### [P3][F8] 已存在 blob 的排空没有上限

- 做法：这条分支同样套 `MaxBytesReader`，超限返回 413，读失败返回 400。
- 位置：`api/v1.go`（`putBlob`）。
- 验证：对一个已存在的 blob 再 PUT 257 MiB，返回 413 `too_large`。

### [P3][F9] HEAD 的 Content-Length 永不生效

- 做法：删除该行，HEAD 只表示存在性；DESIGN 第 6 节已按此语义描述。
- 位置：`api/v1.go`（`headBlob`）。
- 验证：`curl -sI` 响应里没有 `Content-Length`。

### [P3][F10] gc 子命令强制要求面板密钥

- 做法：`main` 先判断子命令，gc 走 `core.DataDirFromEnv()` + `app.OpenData`，完全不加载 `AUTH_TOKEN`/`SESSION_SIGNING_KEY` 与 namespace 配置。
- 位置：`main.go`、`core/config.go`、`app/app.go`。
- 验证：`DATA_DIR=... ./.dist/app-linux-amd64 gc` 直接返回 `garbage collected`，退出码 0。

### [P3][F11] 同一 namespace 内重复 token 静默覆盖

- 做法：`NewTokenIndex` 命中已有摘要时一律报错，跨 namespace 与同 namespace 分别给出错误信息，不再覆盖。
- 位置：`core/token.go`。
- 验证：`probe-config.sh` 两种顺序（`[rw, r]` 与 `[r, rw]`）都拒绝启动；新增 `TestTokenIndexRejectsDuplicates`。

### [P3][F12] 服务端 `ui.*` 文案无人调用

- 做法：删除 15 条 `ui.*`，服务端只保留 `error.*`；DESIGN 第 6 节写明界面文案只由前端词典提供。
- 位置：`i18n/i18n.go`、`DESIGN.md`。
- 验证：`grep '"ui\.' --include=*.go` 命中 0。

### [P3][F13] 死代码

- 做法：删除 `core.Parse` 与 `errMissingSecret`（含 `errors` 导入）。
- 位置：`core/manifest.go`、`core/session.go`。
- 验证：`go build`、`go vet` 通过，`grep` 命中 0。

### [P3][F14] 日志包装让 sendfile 失效

- 做法：`recorder` 增加 `Unwrap`、`ReadFrom`（转发到底层 `io.ReaderFrom`）与 `Flush`。
- 位置：`api/router.go`。
- 验证：代码路径验证（`http.ServeFile` → `io.Copy` → `recorder.ReadFrom` → `*http.response.ReadFrom`）；无 strace，未直接观察 `sendfile` syscall。

### [P3][F15] 文档失效

- 做法：README 的测试 token 改为 `sync_default_rw_2026a` 等实际值；把 `model.svg` 补进 `docs/` 并按新布局改写文字，DESIGN 链接改为 `docs/model.svg`。
- 位置：`README.md`、`DESIGN.md`、`docs/model.svg`。
- 验证：`config.example.yaml` 用新的校验规则能正常启动（timeout 2 秒后退出码 124）。

### [P3][F16] 前端 NaN 请求与错误体当正文

- 做法：`?seq=` 先判断 `Number.isInteger(v) && v > 0`；预览前检查 `response.ok`，失败走本地化的加载失败提示。
- 位置：`frontend/src/components/Panel.vue`。
- 验证：`pnpm typecheck` 与 `pnpm build` 通过；未在浏览器里点选验证。

## 待确认项回应

1. **64 MiB 请求体与 10 万条目的关系**：两者独立。请求体超限现在映射为 413，条目数超限仍是 422 `invalid_manifest`；已写入 DESIGN 第 6 节。
2. **提交时 manifest 全量入内存**：接受现状。同一 namespace 的提交被租约串行化，理论上限是全局限额乘以 64 MiB，实际并发远低于该值；未做压测，保持记录。
3. **`createdAt` 语义变化**：已在 DESIGN 第 6 节写明它是最老保留快照的时间，裁剪会让它前移，仅用于展示。
4. **`identical` 逐行字节比对**：接受。当前只有这一个实现写该目录，F4 之后也不允许混用。
5. **提交时 `List` 打开全部文件**：接受。保留 10 个版本时是常数开销。
6. **`DATA_DIR` chmod 0700**：已写入 DESIGN 第 12 节，这是有意的权限收紧。

## 构建与测试

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test ./... -count=1` | 通过，`core` 12 个顶层测试、31 个用例（原 14 个） |
| `pnpm typecheck` | 通过 |
| `pnpm build` | 通过 |
| 协议冒烟（34 项） | 全部 PASS |
| `probe-api.sh` | 见 F1/F2/F8/F9/F10 各项 |
| `probe-concurrency.sh` | 见 F1/F7 |
| `probe-config.sh` | 见 F11 |
| gc 后引用完整性 | 262,372 条引用全部能找到 blob，缺失 0 |

## 未自动验证

- `docker build` 与 arm64 交叉编译产物；
- 浏览器内的登录跳转、语言切换、树渲染与下载（只跑了类型检查与构建）；
- fsync 与 sendfile 的 syscall 级证据（本机无 strace，仅有代码路径与行为验证）；
- 10 万条目 / 256 MiB blob / `MAX_API_CONCURRENCY=128` 的压测沿用上一轮数据，未重新跑。
