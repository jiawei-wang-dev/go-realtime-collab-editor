# 本地启动与完整功能测试指南

最后验证时间：2026-11-09

## A. 当前运行环境

项目当前已经启动，可以直接访问：

| 服务 | 地址 | 说明 |
|---|---|---|
| 浏览器前端 | `http://127.0.0.1:5173` | 日常操作只需要打开此地址 |
| REST / WebSocket 后端 | `https://127.0.0.1:18080` | 使用本地临时证书；前端通过 Vite 安全代理访问 |
| 管理端点 | `http://127.0.0.1:19090` | `/livez`、`/readyz` 和 `/metrics` |
| QUIC | `127.0.0.1:18443/udp` | TLS 1.3 实时传输 |
| 内部 mTLS RPC | `https://127.0.0.1:19443` | 不对浏览器使用 |
| MySQL | `127.0.0.1:13306` | 独立 Docker Volume |
| Redis | `127.0.0.1:16379` | 独立 Docker Volume |
| etcd | `https://127.0.0.1:12379` | TLS Client Authentication |

运行资源使用独立 Compose Project `collab-local-running`，不会连接或停止其他项目的容器。

### 检查状态

```bash
/tmp/collab-local-running/status.sh
```

也可以直接检查：

```bash
curl http://127.0.0.1:19090/livez
curl http://127.0.0.1:19090/readyz
```

`readyz` 应显示 `mysql`、`redis`、`etcd`、`membership` 和 `node` 均为 `ready`。

### 查看日志

```bash
tail -f /tmp/collab-local-running/backend.log
tail -f /tmp/collab-local-running/frontend.log
```

依赖容器日志：

```bash
cd "/Users/wangjiawei/Desktop/代码管理平台"
docker compose -p collab-local-running \
  --env-file /tmp/collab-local-running/runtime.env \
  -f docker-compose.yml \
  -f /tmp/collab-local-running/docker-compose.override.yml \
  logs -f mysql redis etcd
```

### 停止后的重新启动

在一个单独 Terminal 中运行，并保持该 Terminal 打开：

```bash
/tmp/collab-local-running/supervise.sh
```

该命令会复用现有容器和 Volume，不会重建数据库。

## B. 注册两个测试账号

1. 打开 `http://127.0.0.1:5173`。
2. 点击 **Create account**。
3. 注册用户 A，例如 `alice.local`。用户名必须为 3–32 个字符，只能包含字母、数字、`_`、`-`、`.`；密码为 8–72 个字符。
4. 打开 Account 页面并点击 **Sign out of this session**。
5. 注册用户 B，例如 `bob.local`，然后退出。
6. 在 Chrome 普通窗口登录用户 A。
7. 在 Chrome 无痕窗口打开相同地址并登录用户 B。

不要在文档或截图中记录真实密码。Refresh Token 保存在 HttpOnly Cookie 中，Access Token 只保存在内存。

### 登录状态恢复

用户登录后刷新页面，应返回 Dashboard，而不是登录页。新打开的同一浏览器标签页也应恢复 Session。未登录或已退出的浏览器直接打开 `/documents/<id>` 时应跳转到登录页。

## C. 创建文档与多人实时协作

### 创建文档

1. 在用户 A 的 Dashboard 点击 **New document**。
2. 输入稳定的文档 ID，例如 `demo/main.go`。
3. 点击 **Create and open**。
4. 等待右上角连接状态显示 `connected`，底部显示 `0 pending` 和 `Synced`。
5. 在 Monaco Editor 输入：

```go
package main

import "fmt"

func main() {
    fmt.Println("hello collaboration")
}
```

状态栏的 Version 应递增。刷新页面后内容和 Version 应保持。

当前 UI 支持创建、搜索和删除文档，不支持重命名。Owner 可以在 Dashboard 使用垃圾桶按钮删除文档；删除操作不可撤销。

### 授权用户 B

1. 用户 B 必须已经完成注册。
2. 在用户 A 的编辑器点击 **Share**。
3. 输入用户 B 的完整用户名。
4. 选择 **Can edit**。
5. 点击 **Share**，确认列表出现用户 B 且角色为 `write`。
6. 点击 **Done**。

### 双浏览器协作

1. 用户 A 保持文档打开。
2. 用户 B 刷新 Dashboard；共享文档应出现在列表中。
3. 用户 B 打开同一文档，等待显示 `connected`。
4. 两个窗口的 Collaborators 区域应显示两位在线参与者。
5. A 在文件开头插入注释，B 应实时看到。
6. B 在文件结尾插入代码，A 应实时看到。
7. 两人分别在不同位置输入，确认最终内容一致。
8. 两人同时在同一位置输入不同字符，等待双方均显示 `0 pending`，确认内容一致且没有字符消失。
9. A 选中一段文本，B 应看到带用户名的远程选区和光标；B 移动光标时 A 也应看到对应位置。
10. Cursor/Selection 不应增加文档 Version；只有文本 Insert/Delete 才增加 Version。

打开 DevTools 的 Console 和 Network：除首次未登录时可能出现的匿名 Refresh `401` 外，不应持续出现 JavaScript Error、失败 WebSocket 或未解释的 4xx/5xx。

## D. 断线恢复与 Pending Edit

建议只让用户 A 的浏览器离线，用户 B 保持在线：

1. 两个浏览器都打开同一文档。
2. 在 A 的 DevTools 打开 **Network**，将网络设置切换为 **Offline**。
3. A 的连接状态应变为 `reconnecting`。
4. A 继续在编辑器末尾输入唯一文字，例如 `// offline alice`。
5. 状态栏的 Pending 数量应大于 0。
6. 在 DevTools 的 **Application → IndexedDB → collab-editor → pending-edits** 查看 Pending Record；记下但不要修改其 `requestId`。
7. B 保持在线并输入另一段文字。
8. 把 A 的 Network 恢复为 **No throttling**。
9. A 应自动重连，经历 AUTH、RESUME/SYNC，并最终显示 `connected`、`0 pending`、`Synced`。
10. IndexedDB 中对应 Pending Record 应被删除；A 离线输入和 B 在线输入都应出现在两端。
11. 刷新 A，确认最终内容仍然存在。

浏览器端不会显示 Token。原始 `request_id` 在 Pending 重发期间保持不变，由服务端和 MySQL Operation Log 的唯一约束避免重复应用。

## E. 权限测试

### 动态降级

1. 用户 B 以 `write` 权限打开文档。
2. 用户 A 打开 **Share**。
3. 再次输入用户 B 的用户名，将角色改为 **Can view** 并点击 **Share**。
4. B 的 Access 应立即变为 `read`，编辑器进入只读状态，连接保持在线。
5. B 尝试输入，文档内容和 Version 不应变化。
6. A 继续编辑时，B 仍应实时接收 A 的更新。

当前 UI 支持在 `write` 与 `read` 之间更新权限，但没有删除 Permission 的按钮。

### 越权验证

- 未被共享的第三个用户不应在 Dashboard 看到文档。
- 直接输入文档 URL 应被后端拒绝。
- `read` 用户不能修改权限或提交 Edit。
- 只有 Owner 可以打开 Share 操作和删除文档。

## F. Session 与跨标签页 Logout

1. 使用同一个普通浏览器 Session 打开两个标签页。
2. 一个标签页停留在编辑器，另一个进入 Account 页面。
3. 点击 **Sign out of this session**。
4. 两个标签页都应跳转到 `/auth`。
5. 原 WebSocket 应关闭，其他协作者看到该用户光标消失。
6. 刷新任一标签页，不能恢复为已登录状态。

该流程会撤销 Redis Session；前端通过 BroadcastChannel 同步本浏览器内的退出状态。

## G. 双应用节点说明

当前为一个真实 Cluster 模式应用节点：etcd Membership、Lease、Ownership、MySQL Fencing、内部 mTLS 和 QUIC TLS 均已启用。它足以进行浏览器双用户协作和 Ownership/Fencing 写入路径测试。

本次保持运行的环境没有启动第二个应用节点，因此不要把当前手工测试写成“双节点 Failover PASS”。双节点 Owner Redirect、Owner 退出、自动接管及旧 Fencing Token 拒绝已由项目现有隔离 E2E 覆盖，但如需重新手工演示，应另行启动具有独立 NodeID、Incarnation、端口及 mTLS 证书的 Node B。

## H. 安全停止

运行：

```bash
/tmp/collab-local-running/stop.sh
```

该命令会：

- 优雅停止前端和 Go Backend。
- 停止本项目的 MySQL、Redis、etcd 容器。
- 保留 `collab-local-running_mysql_data`、`collab-local-running_redis_data` 和 `collab-local-running_etcd_data`。
- 不操作其他项目的容器或 Volume。

不要使用 `docker compose down -v`，否则会删除本地验收数据。
