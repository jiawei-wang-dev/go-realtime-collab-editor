# Clean Code 深度审查与可读性重构报告

## 1. 项目整体代码质量评价

当前项目已经具备比较清晰的分层：HTTP Handler 负责协议适配，Room 通过单 goroutine 串行化单文档状态，Storage 封装事务和恢复边界，QUIC 只处理连接、认证和帧，Client Manager 使用单一 supervisor 管理重连。并发所有权、协议边界和数据持久化边界整体明确，并有单元、localhost 真实 QUIC/TLS 和 race 测试支撑。

主要可维护性问题集中在大量阶段性功能尚未提交，导致 Git 工作区很难直接识别边界；部分函数仍同时承担了状态读取、消息构造或资源装配细节；个别响应结构、Token 返回结构和 SQL 行扫描存在重复。本次仅重构可以由现有测试与 race detector 证明安全的部分，没有引入新依赖、新接口或架构层。

## 2. 重构前发现的问题

- `Room.handleEvent` 内联了权限查询和文档读取处理，`handleResume` 内联了 SYNC/RESYNC 消息选择，快照保存和延迟重试使用了较长闭包。
- QUIC `Dial` 内联了全部客户端默认值填充，遮挡了主要连接流程。
- Client Manager 的 Pending 状态构造、JOIN/RESUME 选择和断线后 SENT 状态复位与主流程交织，并存在两个内容相同的 wake signal 方法。
- Authentication 在登录/创建 Session 和 Refresh 路径重复构造 `TokenPair`。
- MySQL 的两个 Operation Log 查询重复扫描同一组列并检查 `rows.Err()`。
- GET/PUT Permission Handler 重复使用匿名 map 构造同一 JSON 契约。
- `cmd/server.run` 同时包含应用装配和 QUIC TLS/监听细节，主启动流程不够突出。
- 旧 `/ws` 实现已在本任务之前删除；当前代码无残留引用，但对旧 WebSocket 客户端属于显式的协议迁移。

## 3. 本次实际修改的文件

| 文件 | 本次修改 |
| --- | --- |
| `internal/room/room.go` | 拆分权限查询、文档读取、Resume 消息构造、快照保存和重试边界 |
| `internal/transport/quic/connection.go` | 提取 Dial 配置默认值填充 |
| `internal/client/manager.go` | 提取 Pending 状态、恢复请求和 SENT 复位，合并 wake signal |
| `internal/authentication/service.go` | 合并 `TokenPair` 构造 |
| `internal/storage/mysql/store.go` | 合并 Operation Log 扫描与迭代错误处理 |
| `internal/api/server.go` | 使用有类型的 Permission Response 代替重复匿名 map |
| `cmd/server/main.go` | 提取应用装配和 QUIC 监听初始化 |
| `docs/CLEAN_CODE_REVIEW_REPORT.md` | 本审查、验证和 Git 隔离报告 |

相对任务开始时保存的完整工作区快照，7 个既有代码文件的本次净变更为 160 行新增、147 行删除；报告是唯一的本次新建文件。

## 4. 各模块优化内容

### Room

- 保留 `handleEdit` 中的严格顺序：已 Join 校验 → 权限 → 幂等 → Version/History → OT → Store Transaction → 内存状态 → Broadcast → Snapshot。
- 将 Permission Query 和 Document Read 从事件 switch 中提取，但仍由 Room event loop 串行执行。
- 将 Resume 的 SYNC/RESYNC_REQUIRED 选择提取为纯消息构造，保留“初始状态先入队、再注册广播”的无缺口顺序。
- 快照 goroutine 改为具名方法，保留 WaitGroup、Timeout、Room 事件回传和取消语义。

### QUIC Transport

- `Dial` 只保留 TLS 校验、ALPN/TLS 1.3 固定、Dial、OpenStream 主流程；默认值填充移入 `applyDialConfigDefaults`。
- 没有改变 ALPN `collab-editor-v1`、TLS 最低版本、一个双向 Stream、禁用 Datagram、Deadline 或帧格式。

### Client Manager

- `statusLocked` 不再内联 Pending DTO 构造。
- `startSession` 通过 `recoveryRequestLocked` 明确选择 JOIN/RESUME，保留 `last_version`。
- 连接失败与连接清理共用 `resetSentPendingLocked`，原 `request_id` 不变。
- 删除内容相同的 `signalLocked`，统一使用非阻塞、容量为 1 的 wake channel signal；仍只有 `NewManager` 启动的一个 supervisor。

### Authentication

- Login/Session 创建和 Refresh 路径共用 `tokenPair`，统一 Bearer 类型和 `expires_in` 计算。
- Refresh Rotation、Replay AES-GCM、Session Gate、Logout 顺序和用户状态重载逻辑均未改动。

### Storage

- `loadOperations` 与 `loadOperationsAfter` 共用 `scanOperations`，保留查询 SQL、列顺序、原有错误文本和 `rows.Err()` 检查。
- MySQL `CommitEdit`、`SetPermission`、Snapshot 事务边界未改动；SQLite `CommitApplied` 和 Replay 也未改动。

### API

- Permission GET/PUT 共用 `permissionResponse`，JSON 字段仍为 `doc_id`/`user_id`/`role`。
- Handler 仍只执行认证、严格 JSON 解码、Room/Service 调用和 HTTP 错误映射。

### Config

- 审查后未修改。默认值、认证/MySQL/QUIC 子配置加载和正整数/时长校验已集中。
- QUIC 超时逐项解析看似重复，但它使每个环境变量和返回错误保持明确；不为减少行数引入反射或隐式配置表。

### Server

- `run` 中的应用构建提取为 `newApplication`，QUIC TLS/配置/监听提取为 `listenQUIC`。
- 保留 Store → Migration → Authentication → Listener → Serve 的初始化顺序，以及 QUIC → HTTP → Room 的关闭顺序。

## 5. 拆分的复杂函数

- `Room.handleEvent` → `handlePermission` / `handleReadDocument`。
- `Room.handleResume` → `resumeMessage`。
- `Room.maybeSaveSnapshot` / `handleSnapshotSaved` → `saveSnapshot` / `retrySnapshotAfter`。
- `quic.Dial` → `applyDialConfigDefaults`。
- `Manager.statusLocked` → `pendingStatusesLocked`。
- `Manager.startSession` → `recoveryRequestLocked`。
- `server.run` → `newApplication` / `listenQUIC`。

这些边界分别对应状态查询、协议消息构造、后台任务、配置默认值和依赖装配；没有创建只为缩短代码的新接口。

## 6. 合并的重复代码

- Client Manager 的两处 SENT → QUEUED 复位。
- Client Manager 的两个相同 wake channel signal 方法。
- Authentication 的两处 `TokenPair` 初始化。
- MySQL 的两处 Operation Log 行扫描。
- API Permission GET/PUT 的两处 JSON 响应初始化。

## 7. 简化的复杂逻辑

- Resume 消息选择改为有名返回函数，主流程只显示权限、入队和注册顺序。
- Client Session 启动流程不再同时处理加锁取值和协议字段切换。
- Server 主流程不再展开 QUIC TLS 文件加载和 Registry 注入细节。
- 快照后台任务用具名方法表达 WaitGroup 与取消边界，避免长闭包。

## 8. 刻意保留原样的代码

- `Room.handleEdit` 没有继续机械拆分：当前 38 行主流程就是事务性业务顺序，过度抽象会隐藏权限、幂等、持久化和广播的先后关系。
- MySQL `CommitEdit` 和 `SetPermission` 没有为缩短代码拆分事务：行锁、幂等、CAS 和 COMMIT 需要在一处可见。
- Authentication Refresh 的 Rotation/Replay 分支保留：这些分支表达安全语义，不适合合并为隐式状态机。
- Config 的逐项环境变量加载保留：明确的变量名和默认值比通用动态解析更容易审计。
- `writeJSON` 在响应头已写入后忽略 Encoder 错误的既有行为未改；如果需要可观测性，应在独立行为修改中设计日志/指标，而不是在可读性重构中更改 HTTP 行为。

## 9. 并发和事务安全检查

- Room 的 document/history/permission/client 状态仍只由事件 goroutine 修改；快照 goroutine 只执行 Store I/O，结果通过 event 返回。
- Client Manager 只有一个 supervisor；wake channel 合并多次信号，不会启动第二个重连循环。Token Manager 的锁和 Refresh 单飞行为未改。
- QUIC Connection 写操作仍由 mutex 串行，Client 关闭仍可重入，Server 客户端 Registry 的锁和 WaitGroup 未改。
- Redis Refresh Rotation 仍通过 Lua 原子更新 Session Hash 和 Replay 记录；Session Gate 仍保证 Logout 等待已授权消息完成。
- MySQL Edit 仍在同一事务内执行 request ID 检查、Version CAS、Document Update 和 Operation Log Insert。SQLite Document Update 与 Pending Delete 仍使用同一事务。
- `go test -count=1 -race ./...` 全部可执行包通过，未报告 data race。

## 10. WebSocket 删除影响分析

- 本任务开始时 `internal/websocket/client.go` 和 `handler.go` 已是 tracked deletion，本次没有读写、恢复、暂存或提交它们。
- 当前 Go 代码和 README 中没有 WebSocket 运行时引用，`go.mod` 中的实时传输已转向 QUIC。
- 旧 WebSocket 只支持 JOIN/EDIT，使用 query `user_id` 且 `CheckOrigin` 无条件允许。当前 QUIC 使用 JWT AUTH、TLS 1.3、JOIN/RESUME/EDIT、Permission Changed、SYNC/RESYNC、Session 撤销和严格 framing，功能和安全性超过旧实现。
- localhost 真实 QUIC/TLS 测试覆盖认证、JOIN、EDIT、广播、读权限、Logout/Session 撤销、Resume/Sync/Resync、恶意帧、并发发送和 TLS 校验。
- 结论：对当前“QUIC 作为唯一实时传输”的目标架构，删除内部实现是合理的；但 `/ws` HTTP 端点不再存在，对旧客户端不兼容。在宣布对外协议无缝升级前，仍需产品/迁移决策确认旧客户端已下线，或明确提供独立兼容期。

## 11. 测试结果

| 验证 | 结果 |
| --- | --- |
| 本次文件 `gofmt` | PASS，`gofmt -l cmd internal` 无输出 |
| Room/Collab/Storage 定向测试 | PASS |
| Room race | PASS |
| QUIC/Transport 定向测试 | PASS |
| QUIC race | PASS |
| Client 定向测试 | PASS |
| Client race | PASS |
| Authentication 定向测试 | PASS |
| Authentication race | PASS |
| Storage/SQLite 定向测试 | PASS |
| Storage/SQLite race | PASS |
| API 定向测试 | PASS |
| API race | PASS |
| Config 定向测试 | PASS |
| Config race | PASS |
| Server 定向测试 | PASS |
| Server race | PASS |
| localhost 真实 QUIC/TLS Integration | PASS |
| `go vet ./...` | PASS |
| `go test -count=1 ./...` | PASS（需外部依赖的测试按条件 SKIP） |
| `go test -count=1 -race ./...` | PASS（需外部依赖的测试按条件 SKIP） |
| `go build ./...` | PASS |
| `git diff --check` | PASS |

真实集成环境状态：

- `MYSQL_TEST_DSN`：missing。
- `REDIS_TEST_ADDR`：missing。
- 定向 `-v` 运行共记录 20 条 `SKIP` 输出，包括 Migration、MySQL Store/User Repository、Redis Session/Rotation 和 MySQL+Redis+QUIC+SQLite 崩溃恢复。
- 未启动、删除或修改 Docker 容器/Volume，未连接任何未确认的数据库。SKIP 不计为真实 MySQL/Redis PASS。

## 12. 未解决问题

1. **外部事务真实性未在本环境重验**：MySQL/Redis 集成变量未配置，因此事务锁、Lua 原子性和跨组件崩溃恢复只能依据代码审查与现有测试，不得宣称已在真实外部服务上 PASS。
2. **WebSocket 对外兼容需人工决策**：QUIC 已承接当前业务能力，但旧 `/ws` 客户端无透明兼容层。
3. **Redis Lua 返回形状的防御性错误**：`RedisSessions.RotateReplay` 在 `err == nil` 但 Lua 返回长度不是 3 时会返回 nil error。当前固定 Lua 脚本的所有分支都返回 3 项，所以不影响现有路径；建议在独立健壮性修复中返回明确的“unexpected Redis result”错误并添加测试。
4. **Migration 运行器的部分应用风险**：当前 SQL 语句逐条执行，最后记录 `schema_migrations`。中途失败可能留下部分 DDL；MySQL DDL 的隐式提交也使简单事务包装不足以解决。这需要独立迁移策略决策，本次不改变 Schema 行为。
5. **Server 早期 Listener 释放**：HTTP listener 在 QUIC TLS/监听初始化之前已创建，后续初始化失败时未显式关闭。这是启动失败路径的资源释放问题，建议以单独修复和失败注入测试处理；本次不在可读性重构中暗中改变生命周期行为。

## 13. Git 变更隔离报告

### 开始基线

- Branch：`main`
- HEAD：`0c349f0c7b9babd62dabf8d7cd01c107d4e69621`
- 仓库仅有 3 条可见 commit：`0c349f0`、`bb15085`、`9d88d4f`。
- Staged：空。
- Conflict：无。
- 开始时已有 17 个 tracked 状态条目（15 modified + 2 deleted）和 20 个 untracked 顶层条目；详细列表由任务开始快照保存。
- 开始时 tracked diff stat：17 files，2372 insertions，1250 deletions。

### 任务隔离方法

- 开始时在仓库外创建只读快照目录 `/tmp/clean-code-audit.vSFjZ2`，保存 `git status`、tracked/staged patch、SHA-256 清单和工作树文件副本。
- 通过逐文件 byte comparison 确认本次只改动上述 7 个既有代码文件，并新增本报告。
- 之前已有的 tracked/untracked 成果未被删除、清理、暂存或提交。
- 本次未执行 `git add`、`git commit`、`git push`、`git reset`、`git clean`、`git restore`、`git checkout`、`git stash`。

### 完成时状态

- Branch 仍为 `main`，HEAD 仍为 `0c349f0c7b9babd62dabf8d7cd01c107d4e69621`。
- Staged 仍为空，Conflict 仍为无。
- 两个 tracked WebSocket deletion 仍保持任务开始时状态。
- 原有 untracked 顶层条目均保留；新增 `docs/CLEAN_CODE_REVIEW_REPORT.md`。
- Commit：NO。
- Push：NO。

## 最终结论

- **CLEAN CODE REVIEW：PASS**
- **READABILITY REFACTORING：PASS**
- **BEHAVIOR COMPATIBILITY：PASS**（相对本任务开始时的工作区行为；旧 `/ws` 对外兼容风险单独列出）
- **CONCURRENCY SAFETY：PASS**
- **TESTS：PASS**（真实 MySQL/Redis 条件测试为 SKIP，未虚报）
- **GIT WORKSPACE PRESERVATION：PASS**
- **COMMIT：NO**
- **PUSH：NO**
