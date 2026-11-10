# Phase 10.11 Final Release Report

执行日期：2026-11-10

基线分支：`main`

基线 HEAD：`19ee3ec3a585a24e13f3c7d5a1470cfc5118ac32`

## 审计范围

本次审计覆盖 Phase 10.9 的 Go 单文件在线运行功能，以及 Phase 10.10 的中英文 README 和六张真实运行截图。未修改数据库 Schema、Terraform、真实 `.env` 或任何既有 Docker Volume，也未执行 Google Cloud 部署。

## Phase 10.9 代码审查

- 浏览器发送 Monaco 当前 Buffer，不从持久化文档重新读取源码。
- API 先执行 JWT/Session 认证和 `owner`/`write` 权限检查；`read` 用户返回 HTTP 403。
- API 进程不持有 Docker Socket，只通过权限为 `0600` 的 Unix Socket 调用独立 Runner。
- API 与 Runner 使用至少 32 字节的共享 Token；Runner 使用常量时间比较验证 Bearer Token。
- 源码只通过 stdin 传入固定容器命令，不参与 Shell、容器名、路径或 Docker 参数拼接。
- 每次请求使用随机容器名和临时容器；结束、超时或调用方断开时均尝试强制清理。
- Sandbox 强制无网络、只读 RootFS、非 Root UID/GID、移除全部 Capability、`no-new-privileges`、有界 tmpfs、512 MiB Memory、1 CPU、64 PID、10 秒超时和 stdout/stderr 共享 64 KiB 上限。
- 容器不挂载项目目录、宿主机路径或 Docker Socket；只注入固定 Go 运行环境变量，不传递 API/MySQL/Redis/JWT Secret。
- 用户并发、节点并发和时间窗口 Rate Limit 均在调用 Runner 前执行。
- Run 走 `Execution API → Runner → Sandbox`，不进入 Room、OT、Snapshot 或 Operation Log，也不改变文档 Version。

审计未发现命令注入、路径穿越、未授权 Runner 调用或绕过现有协作/Fencing 链路的问题。

## Phase 10.10 README 与截图审查

- `README.md` 与 `README.zh-CN.md` 各有 36 个对应章节标题，技术版本、启动命令、限制和架构说明一致。
- Mermaid 明确区分协作链路和 `Execution API → Runner → Sandbox` 执行链路。
- 两份 README 的语言切换、六张图片路径、内部文档链接、表格和折叠区均通过 GitHub GFM API 解析。
- Go Runner 的公开能力如实限制为单个 Go 源文件、标准库和非交互式执行；未宣传多文件、第三方 Module、Shell、Terminal 或其他语言。

## 截图隐私检查

逐张打开并检查：

| 文件 | 结果 |
|---|---|
| `dashboard.png` | 文档列表、演示用户名和 Role 清晰，无私人数据 |
| `collaborative-editor.png` | Monaco、连接状态、参与者和 Version 正常，无异常界面 |
| `go-code-execution.png` | Run、stdout、`Exited 0` 和耗时均为真实运行结果 |
| `realtime-collaboration.png` | 两个独立 Session 的远程光标和选区真实可见 |
| `execution-error.png` | 真实 `undefined: fmt` stderr、非零 Exit Code 和耗时可见 |
| `login.png` | 密码完全遮蔽，无 Cookie、Token 或浏览器导航栏 |

六张文件均为有效 PNG、权限为 `0644`。静态 Secret 扫描及视觉检查均未发现凭据泄露。

## 质量 Gate

| Gate | 结果 |
|---|---|
| `gofmt -l .` | PASS，无输出 |
| `go test -count=1 ./...` | PASS |
| `go test -count=1 -race ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `git diff --check` | PASS |
| `go mod verify` | PASS |
| `govulncheck ./...`（`golang.org/x/vuln@v1.9.0`） | PASS，调用路径 0 漏洞 |
| `npm run lint` | PASS |
| `npm run typecheck` | PASS |
| `npm test -- --run` | PASS，6 files / 13 tests |
| `npm run build` | PASS；仅有 Monaco Bundle 体积提示 |
| `npm audit --audit-level=high` | PASS，0 vulnerabilities |

## 真实 Docker Runner 验收

使用固定镜像 `golang:1.26.10-bookworm` 实际运行 `TestDockerRunnerRealSandbox`，所有子测试 PASS：正常输出、循环输出、编译错误、runtime panic、CPU/Memory/PID/只读/网络参数检查、无限循环超时、网络和 Secret 隔离、输出截断、宿主机路径隔离、内存超限、并发执行隔离及最终容器清理。测试结束后不存在带 `com.collab-editor.sandbox=true` Label 的残留容器。

真实 HTTP 验收结果：Viewer 为 403；同用户并发执行为 429；无限循环返回 `timed_out=true`；Run 前后文档 Version 均为 0。

## Chromium E2E

- `frontend/e2e/execution.spec.ts`：PASS，真实登录、Dashboard、Monaco Buffer、stdout、编译错误、Exit Code、Output Clear 和 Version 不变。
- `frontend/e2e/collaboration.spec.ts`：PASS，两个独立 Browser Context 的 OT、光标/选区、离线 Pending、Session 恢复、动态权限和 Logout 均未受 Run 功能影响。
- E2E 产生的旧 Phase 10.4 截图副作用已从基线 Git 对象机械恢复，未纳入本阶段提交。

## 真实依赖集成测试

使用本阶段专属、无 Volume 的 MySQL 8.4.13、Redis 7.4.5 和 etcd 3.5.17 容器，以及新生成的短期 TLS/mTLS 测试证书。以下测试全部真实执行且无 Skip：

- Fresh/升级/中断恢复/并发 Migration。
- MySQL 原子编辑、回滚、幂等、Fencing、Snapshot + Replay 和 Room 重建。
- Redis Session、Presence、Operation Lease、Pub/Sub 恢复、跨节点 Logout 和 fail-closed。
- etcd 分布式测试锁、Membership、Ownership、网络分区、自我 Fencing、接管和三节点恢复。
- mTLS Permission 转发、证书身份校验和旧 Owner 拒绝。
- QUIC TLS、Logout、RESUME/SYNC、Message Limit 和并发 Framing。
- MySQL + Redis + QUIC + SQLite 完整进程崩溃恢复。

临时容器、网络和短期证书已清理；既有 `collab-local-running` 环境仍保持 Ready，未操作任何其他项目容器或 Volume。

## Commit 与 GitHub

- Phase 10.9 Commit：`bbe8de71f15ebb07734fcf6c4349407f3764cf29`（`feat: add isolated Go code execution`）。
- Phase 10.10/10.11 文档 Commit：本报告所在提交；由于 Git Commit 不能在不改变自身 Hash 的情况下嵌入自身最终 Hash，最终值记录在 GitHub History 和本次任务最终回复中。
- Push 与 GitHub 页面验收结果以本次任务最终回复及远端 `main` 为准。

## 剩余风险

- Vite 生产构建提示 Monaco 相关 Bundle 较大；当前功能正确，但后续可通过 Worker/Language 动态加载降低首屏体积。
- Sandbox 依赖宿主机 Docker Daemon。公网 API 不持有 Docker 权限，但生产部署仍需要独立加固 Runner Tier；当前 Terraform 未部署该 Tier。
- 浏览器执行只支持单 Go 文件和标准库，冷编译时间计入 10 秒上限。
- 原生 Safari 自动化仍未验证；Chromium、Firefox 与 Playwright WebKit 的既有测试结果不等同于原生 Safari。
