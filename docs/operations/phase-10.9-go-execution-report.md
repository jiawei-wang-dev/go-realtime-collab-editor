# Phase 10.9 Go 单文件运行验收报告

## 1. 实现结果

Phase 10.9 完成了浏览器 Go 单文件运行链路。Monaco 工具栏中的 **Run** 按钮只在 `.go` 文档中显示，并且只有 `owner` 和 `write` 可用。请求使用 Monaco `getValue()` 取得当前完整 Buffer，而不是读取数据库中的旧内容。输出面板分开显示 stdout、stderr、Exit Code、Execution Time、Timeout 和输出截断状态，支持 Clear，且不破坏编辑器布局。

同时将 Monaco 从 `@monaco-editor/react` 默认的外部 CDN 加载改为已锁定的本地 `monaco-editor@0.57.0`，解决限制网络下页面一直停在 `Loading Monaco…` 的问题。

## 2. API 与权限

```http
POST /api/v1/documents/{documentID}/run
Authorization: Bearer <access-token>
Content-Type: application/json
```

API 复用现有 JWT Authenticator，因此会同时验证 Redis Session 与用户状态。随后通过 Storage 读取文档权限，只接受 `owner`/`write`；`read` 返回 HTTP 403，不会调用 Runner。请求体有硬上限，语言必须为 `go`，源码不能为空且不能超过 128 KiB。

每用户最多 1 个并发任务，每节点默认最多 4 个，每用户在现有 Rate Limit Window 内默认最多 10 次。限制返回 HTTP 429。Runner 未配置或不可用时 fail closed 并返回 HTTP 503，不会降级为宿主机直接执行。

## 3. Runner 安全边界

`cmd/server` 只通过带 Bearer Token 的 Unix Socket 调用 `cmd/runner`；Socket 权限为 `0600`。只有 Runner 可以调用 Docker，API 进程和 API 容器不需要 Docker Socket。Runner 不会删除占用 Socket 路径的普通文件，只会清理确认为 Unix Socket 的旧端点。

每次运行使用固定 `golang:1.26.10-bookworm` 镜像创建独立容器，并强制：

- UID/GID `65532:65532`，非 Root。
- `--network none`，无法访问外网、MySQL、Redis 或 etcd。
- RootFS 只读，不挂载项目、宿主机路径或 Docker Socket。
- `--cap-drop ALL` 与 `no-new-privileges:true`。
- 512 MiB Memory，禁止额外 Swap；1 CPU；64 PID。
- `/workspace` 16 MiB tmpfs，`/tmp` 256 MiB tmpfs；两者均为 `nosuid,nodev`。
- 单次 10 秒，超时后强制终止并再次执行容器清理。
- stdout/stderr 共享 64 KiB Budget，超出部分丢弃且返回 `output_truncated=true`。
- `GOPROXY=off`、`GOSUMDB=off`，不允许下载第三方 Module。
- 源码通过 stdin 写入固定的 `/workspace/main.go`，不与 Shell 或 Docker 参数拼接。

## 4. 功能与安全测试

| 场景 | 实际结果 |
| --- | --- |
| A. Hello World | PASS，stdout 正确，Exit Code 0 |
| B. 1–5 循环输出 | PASS，顺序与内容正确 |
| C. 编译错误 | PASS，stderr 包含 `undefined: fmt`，服务保持健康 |
| D. Runtime panic | PASS，stderr 包含 `panic: test panic`，Exit Code 非 0 |
| E. 无限循环 | PASS，到期强制终止，`timed_out=true`，容器已删除 |
| F. 网络隔离 | PASS，容器无法连接外部 IP，应用 Secret 环境变量不存在 |
| G. 大量输出 | PASS，stdout/stderr 合计到上限即截断 |
| H. Viewer | PASS，真实 API 返回 HTTP 403，Runner 不执行 |

额外真实 Docker 验收已通过：宿主机项目路径不可读、1 GiB 分配在 512 MiB 限制下失败、两个并发容器输出隔离、Docker inspect 确认 CPU/Memory/PID/Readonly/Network/Capability/SecurityOpt/User 的运行时配置、测试结束后 `com.collab-editor.sandbox=true` 容器数为 0。

## 5. 端到端验收

- 使用真实 MySQL 8.4.13、Redis 7.4.5、TLS etcd、JWT/Redis Session、HTTPS API 和独立 Runner 运行真实 API 测试。
- Owner Run 成功；Viewer Run 返回 403。Run 前后 MySQL 中文档 Version 仍为 0，Content 字节完全一致。
- 同一用户的第一个无限循环任务运行期间，第二个真实 API 请求立即返回 HTTP 429；第一个任务到期返回 `timed_out=true`。
- Playwright Chromium 真实 E2E 完成注册、创建 `.go` 文档、Monaco 加载、修改当前 Buffer、Run、stdout、编译错误、Exit Code/Time 和 Clear 验证。
- 实际 E2E 时使用的临时测试用户与本地数据不包含真实凭据；Token 没有输出或写入仓库。

## 6. 质量 Gate

| Gate | 结果 |
| --- | --- |
| `go test -count=1 ./...` | PASS |
| `go test -count=1 -race ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| 真实 Docker Sandbox（11 个子测试） | PASS |
| `npm run lint` | PASS |
| `npm run typecheck` | PASS |
| `npm test` | PASS，6 files / 13 tests |
| `npm run build` | PASS |
| Playwright `execution.spec.ts` | PASS，1 test |
| `git diff --check` | PASS |

## 7. 当前限制

- 只支持 Go 单文件与标准库，不支持第三方依赖、多文件项目、其他语言、Shell 或交互式 Terminal。
- 冷编译也计入 10 秒超时；在负载较高的开发机上，引用体积较大的标准库可能超时。
- 当前为本地宿主机 Runner 方案。Google Cloud Terraform 没有部署生产 Runner 层，没有执行云部署。
- 用户级 Rate Limit 在每个应用节点内计算；本阶段没有增加跨节点分布式执行调度。

## 8. 数据与 Git 安全

本阶段没有修改数据库 Schema、Room、OT、Ownership、Fencing 或现有协议。没有修改真实 `.env`，没有删除现有 Docker Volume，没有操作其他项目容器，没有 commit 或 push。真实 Docker 测试使用的所有临时执行容器均已自动删除；固定 Go 镜像作为后续本地运行依赖保留。
