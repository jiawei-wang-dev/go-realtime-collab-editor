# 本地运行与完整功能验收报告

日期：2026-11-09

## 1. 环境检查

- 项目分支：`main`
- 启动前 HEAD：`19ee3ec3a585a24e13f3c7d5a1470cfc5118ac32`
- Go：`go1.26.10 darwin/arm64`
- Node.js：`v24.19.0`
- npm：`11.17.0`
- Docker Engine：`29.2.1`
- 当前项目使用独立 Compose Project `collab-local-running`。
- 主机原有 3306、6379、9090 端口由其他项目使用，未停止或复用对应进程。
- 用户真实 `.env` 未修改。

## 2. 实际运行架构

本次启动一个真实 Cluster 模式应用节点，而不是 Memory 或 Development Auth 简化模式：

- MySQL 8.4.13：`127.0.0.1:13306`
- Redis 7.4.5：`127.0.0.1:16379`
- etcd 3.5.17：`https://127.0.0.1:12379`
- Go REST / WebSocket：`https://127.0.0.1:18080`
- QUIC TLS：`127.0.0.1:18443/udp`
- Internal mTLS RPC：`https://127.0.0.1:19443`
- Management：`http://127.0.0.1:19090`
- React / Vite：`http://127.0.0.1:5173`

MySQL、Redis、etcd 使用本项目独立命名 Volume。证书和随机运行凭据仅保存在权限为 600/700 的 `/tmp/collab-local-running`，没有写入仓库、`.env`、README 或日志。

## 3. 依赖与 Migration

- MySQL Container：healthy
- Redis Container：healthy，认证 PING 返回 PONG
- etcd TLS Endpoint：healthy
- etcd Membership Key：1 个 READY Node
- MySQL Migration：PASS，共 6 张项目表
- 后端 `/readyz`：`mysql`、`redis`、`etcd`、`membership`、`node` 全部 ready
- MySQL 当前文档：2
- Operation Log：11
- `document.version != operation count`：0
- Redis 当前 Key：16

## 4. 后端与前端启动结果

- Backend 日志包含 `node_registered` 和 `service ready`。
- REST 使用 TLS，QUIC 使用 TLS 1.3，etcd 使用 Client Certificate，内部 RPC 使用 mTLS。
- QUIC UDP 端口、REST TCP 端口、管理端口和 Vite 端口均确认处于监听状态。
- 前端 `/auth` 实际返回成功。
- 浏览器通过 Vite 代理访问本地自签名 HTTPS 后端，因此用户不需要修改系统证书信任设置。

## 5. 真实功能验收

### 双浏览器 E2E

执行：

```bash
E2E_BASE_URL=http://127.0.0.1:5173 \
  npx playwright test e2e/collaboration.spec.ts --reporter=line
```

结果：`1 passed`，实际使用两个独立 Browser Context，覆盖：

- 用户注册和登录。
- 文档创建、打开、刷新和持久化。
- WebSocket AUTH/JOIN/EDIT。
- 双用户不同位置和相同位置 OT。
- Remote Cursor 与 Selection。
- 离线 Pending 写入 IndexedDB。
- 恢复网络后的重连、Pending 清除和 `request_id` 保持。
- Permission `write → read` 动态降级。
- 跨标签页 Logout。
- 浏览器 Console Error：0。
- Page Error：0。
- 非预期 Network Failure：0。

### QUIC

使用随机测试用户和文档执行真实 CLI 连接：

```text
CONNECTING → AUTHENTICATING → SYNCING → CONNECTED
```

收到 `joined`，随后 Insert 成功产生 `edit_applied`，Server Version 从 0 增加到 1，Pending Operations 为空。Token 和密码没有输出或写入报告。

### 双节点

本次保持运行的环境只有一个应用节点，因此双节点手工验收标记为 **NOT EXECUTED**，没有把单节点 Cluster 测试伪报为双节点 PASS。当前节点仍真实使用 etcd Membership、Ownership Lease 和 MySQL Fencing Token。

## 6. 启动问题与修复

1. **Compose 端口重复合并**：基础 Compose 与 Override 同时保留相同 Published Port，Docker 报端口占用。改为 Compose `!override` 明确替换端口列表。
2. **运行环境文件解析**：MySQL DSN 含括号，不能直接 `source`；Base64 Replay Key 末尾 `=` 也不能用简单 `IFS='=' read`。改为按完整行取第一个 `=` 前后的 Key/Value，不使用 `eval`。
3. **8080 被系统进程占用**：只读确认占用者后未结束该进程，将本项目 REST 改到空闲的 18080，并同步 Vite 与 Membership Endpoint。
4. **工具执行环境清理后台子进程**：单次命令返回后，后台 Vite/Backend 被工具进程组回收。改为前台 Supervisor 持有两个进程并监控退出，不改变项目代码。
5. **E2E 截图副作用**：验收测试重新生成了已跟踪截图。测试结束后只将这 7 个本轮产生的二进制差异恢复为当前 HEAD 内容，未覆盖任何用户原有未提交修改。

所有修复均位于 `/tmp/collab-local-running` 的本地编排文件；没有修改 Go、React、数据库 Schema 或协议。

## 7. 最终质量 Gate

| Gate | 结果 |
|---|---|
| `gofmt -l .` | PASS |
| `go test -count=1 ./...` | PASS |
| `go test -count=1 -race ./...` | PASS |
| `go vet ./...` | PASS |
| `go build ./...` | PASS |
| `npm run lint` | PASS |
| `npm run typecheck` | PASS |
| `npm test -- --run` | PASS，5 files / 11 tests |
| `npm run build` | PASS |
| 双浏览器真实 E2E | PASS |
| QUIC TLS / JOIN / EDIT | PASS |

## 8. 修改文件

仓库内仅新增本次要求的文档：

- `docs/operations/local-running-and-testing-guide.md`
- `docs/operations/local-running-report.md`

此前未跟踪的 `docs/operations/phase-10.8-github-release-report.md` 保持不变。没有 Commit、Push 或 Terraform Apply。

本地运行文件位于 `/tmp/collab-local-running`，其中包括启动、停止、状态脚本、临时证书、运行凭据和日志。它们不属于 Git 仓库。

## 9. 当前 Git 状态

- 已跟踪源码与 `origin/main` 一致。
- 没有 Go、Frontend、Docker、Migration 或 Terraform 变更。
- 未跟踪文件为 Phase 10.8 报告及本次两份运行文档。
- Commit：NO
- Push：NO

## 10. 重新启动与停止

重新启动（在独立 Terminal 前台运行）：

```bash
/tmp/collab-local-running/supervise.sh
```

检查：

```bash
/tmp/collab-local-running/status.sh
```

安全停止且保留数据库 Volume：

```bash
/tmp/collab-local-running/stop.sh
```

浏览器操作步骤见 [本地启动与完整功能测试指南](local-running-and-testing-guide.md)。
