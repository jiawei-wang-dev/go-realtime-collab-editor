# Go Execution Runner / Go 执行 Runner

## Architecture / 架构

The browser sends the current Monaco buffer to `POST /api/v1/documents/{documentID}/run`. The API reuses JWT authentication and Redis session validation, reads the persisted document permission, and accepts only `owner` or `write`. It then applies source-size, rate, and concurrency limits and calls a separate Runner through a mode-`0600`, bearer-authenticated Unix socket. The API process never receives Docker access.

浏览器将 Monaco 当前 Buffer 发送到 `POST /api/v1/documents/{documentID}/run`。API 复用 JWT 认证和 Redis Session 校验，读取持久化的文档权限，仅允许 `owner` 或 `write`。随后 API 执行源码大小、速率和并发限制，通过权限为 `0600` 且使用 Bearer Token 认证的 Unix Socket 调用独立 Runner。API 进程不获取 Docker 权限。

The request source and result are transient. This path does not call Room, OT, `CommitEdit`, snapshots, or operation logs, and therefore never changes document content or version.

请求源码和结果均为临时数据。该链路不调用 Room、OT、`CommitEdit`、Snapshot 或 Operation Log，因此不会改变文档内容和 Version。

## Sandbox / 沙箱

Each run creates an auto-removed container from the pinned `golang:1.26.10-bookworm` image. The Runner passes source through stdin and uses a fixed internal command; no source text is interpolated into a shell or Docker argument.

每次运行都使用固定的 `golang:1.26.10-bookworm` 镜像创建自动删除的容器。Runner 通过 stdin 传递源码并使用固定内部命令；源码不会拼接进 Shell 或 Docker 参数。

| Boundary / 边界 | Default / 默认值 |
| --- | ---: |
| Language / 语言 | Go, one file / 单文件 |
| Source / 源码 | 128 KiB |
| Combined stdout + stderr / 合计输出 | 64 KiB |
| Timeout / 超时 | 10 s |
| Memory / 内存 | 512 MiB |
| CPU | 1 core |
| PIDs | 64 |
| Workspace tmpfs | 16 MiB |
| Temporary/build tmpfs | 256 MiB |
| Concurrent runs per user / 每用户并发 | 1 |
| Concurrent runs per node / 每节点并发 | 4 |
| User rate / 用户速率 | 10 per configured rate window |

Containers run as UID/GID 65532 with `--network none`, a read-only root filesystem, all Linux capabilities dropped, `no-new-privileges`, no project/host mounts, no Docker socket, and bounded tmpfs mounts. `GOPROXY=off` and `GOSUMDB=off` prevent module downloads. `/tmp` allows execution only because `go run` must start its compiled binary; it remains `nosuid`, `nodev`, isolated, and size-limited.

容器使用 UID/GID 65532 运行，并配置 `--network none`、只读 RootFS、移除全部 Linux Capability、`no-new-privileges`、禁止项目/宿主机挂载、禁止 Docker Socket，以及有界 tmpfs。`GOPROXY=off` 和 `GOSUMDB=off` 阻止 Module 下载。由于 `go run` 必须启动编译产物，`/tmp` 需要允许执行；它仍保持 `nosuid`、`nodev`、独立且受大小限制。

## Startup / 启动

```bash
docker pull golang:1.26.10-bookworm
export RUNNER_SOCKET=/tmp/collab-editor-runner.sock
export RUNNER_TOKEN="$(openssl rand -hex 32)"
go build -o /tmp/collab-runner ./cmd/runner
go build -o /tmp/collab-server ./cmd/server
/tmp/collab-runner &
runner_pid=$!
/tmp/collab-server
kill "$runner_pid"
```

`RUNNER_SOCKET` and `RUNNER_TOKEN` are optional as a pair. When absent, the collaboration server still starts and the run endpoint returns HTTP 503. Never write the token to Git, logs, README examples with a real value, or browser code. The Runner verifies that the pinned image already exists and does not pull mutable images at request time.

`RUNNER_SOCKET` 与 `RUNNER_TOKEN` 必须同时配置或同时留空。未配置时协作服务仍可启动，Run API 返回 HTTP 503。不得将真实 Token 写入 Git、日志、README 示例或浏览器代码。Runner 会检查固定镜像已存在，不在请求时拉取可变镜像。

## Verification / 验证

```bash
RUN_DOCKER_INTEGRATION=true \
  go test -count=1 -run TestDockerRunnerRealSandbox -v ./internal/execution

cd frontend
npm run lint
npm run typecheck
npm test
npm run build
npx playwright test e2e/execution.spec.ts
```

The Docker integration suite validates a successful program, loop output, compile errors, panic, forced timeout, network/secret isolation, shared output truncation, host-path isolation, memory enforcement, concurrent isolation, and final container cleanup. A skipped Docker test is not a pass.

Docker 集成测试覆盖正常程序、循环输出、编译错误、panic、强制超时、网络/Secret 隔离、共享输出截断、宿主机路径隔离、内存限制、并发隔离和最终容器清理。Docker 测试 Skip 不能计为 PASS。

## Current Limitations / 当前限制

Only one Go source file and the standard library are supported. Cold compilation is included in the timeout. This local design intentionally requires a host Runner with Docker access; it does not add Docker access to the API container. The current Terraform does not deploy a production Runner tier, so cloud execution remains un-deployed.

当前只支持单个 Go 源文件与标准库，冷编译时间计入超时。本地方案有意要求一个可访问 Docker 的宿主机 Runner，而不是向 API 容器授权。当前 Terraform 未部署生产 Runner 层，因此云端代码执行尚未部署。
