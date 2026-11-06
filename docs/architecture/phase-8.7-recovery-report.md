# Phase 8.7 Recovery Report

## 阶段结果

```text
PHASE: 8.7 Recovery
STATUS: PASS

CRITICAL: 0
HIGH: 0
```

## 已实现内容

### REST HTTPS

- 新增 `REST_TLS_CERT_FILE` 和 `REST_TLS_KEY_FILE`。
- Production 强制配置两个文件；缺失或只配置一个时启动失败。
- Development/Test 只有在明确不配置证书时才使用 HTTP。
- REST TLS 最低版本为 TLS 1.2，兼容 TLS 1.3。
- 启动时验证证书与私钥匹配、证书有效期和 Server Authentication EKU。
- TLS Listener 继续使用原有 HTTP Server 超时与 Graceful Shutdown 流程。
- 不信任客户端提供的 `X-Forwarded-*` Header；未来使用云 TLS 终止时必须另行建立
  可信代理边界。

### NodeID + Incarnation 证书绑定

- Internal RPC 使用规范 URI SAN：
  `spiffe://collab-editor/node/<node-id>/incarnation/<incarnation>`。
- URI 必须使用固定 trust domain、规范路径且没有 query、fragment、userinfo、
  RawPath 或编码歧义。
- 证书必须同时允许 Client Authentication 和 Server Authentication。
- 服务端从 TLS 已验证的 Peer Certificate 提取 NodeID/Incarnation，不再把请求头
  当作身份来源。
- 证书身份必须与 READY etcd Membership 完全匹配。
- `NewClient` 与服务启动均校验证书身份和本地 Node 配置一致。
- Production Cluster 要求显式 `NODE_INCARNATION`，并由外部 CA 签发匹配的新证书；
  当前没有伪造或声称存在自动 CA 轮换。

### Document / Permission Owner Routing

- Public Document GET 与 Permission GET 统一调用 OwnerResolver。
- 本地 Owner 继续经过 Room event queue 和原权限检查。
- 非 Owner 节点使用可信 READY Membership 中的 Internal HTTPS Endpoint 转发。
- Owner 节点重新验证 Bearer JWT、Redis Session、用户状态与文档权限。
- 内部读取 Handler 不再转发，避免转发循环。
- Owner 不存在、非 READY、不可达、mTLS 失败或 Ownership 不确定时返回
  SERVICE_UNAVAILABLE，不回退本地或返回不可信数据。
- 读取完成后再次确认本地 Owner 为 ACTIVE，降低 Ownership 切换期间返回旧状态的
  风险。

## 测试结果

```text
UNIT TEST: PASS
INTEGRATION TEST: PASS
RACE DETECTOR: PASS
GO VET: PASS
GO BUILD: PASS
GIT DIFF CHECK: PASS

REAL MYSQL: PASS
REAL REDIS: PASS
REAL ETCD: PASS
REAL MTLS: PASS
REAL QUIC: PASS
```

真实测试使用隔离的 MySQL 8.4.13、Redis 7.4.5 和 etcd 3.5.17 容器，独立 Docker
Network、临时端口、临时凭据和临时 CA。没有挂载或修改用户现有 Volume。

覆盖内容包括：

- HTTPS 握手、未知 CA、过期证书、错误私钥和 TLS Graceful Shutdown。
- 正确 NodeID/Incarnation、旧 Incarnation Header 冒充、非 READY/未注册节点、
  不可信客户端 CA。
- Node A 接收请求、Node B 持有 Ownership、跨节点 Document/Permission 查询。
- JWT 与 Redis Session 二次验证、无权限用户拒绝、Logout 后 Token 拒绝。
- Owner 不可用和 Fencing 失效时 fail-closed。
- MySQL 事务、Snapshot/Replay、etcd Lease/Failover、QUIC/SQLite Resume。

## 文件清单

### 修改

- `.env.example`
- `README.md`
- `cmd/server/main.go`
- `cmd/server/main_test.go`
- `internal/api/server.go`
- `internal/api/server_test.go`
- `internal/api/internal_permission.go`
- `internal/api/cluster_permission_integration_test.go`
- `internal/cluster/node.go`
- `internal/cluster/routing.go`
- `internal/cluster/rpc/client.go`
- `internal/cluster/rpc/tls.go`
- `internal/config/config.go`
- `internal/config/config_test.go`
- `docs/architecture/phase-8.7-system-review.md`

### 新增

- `internal/api/internal_permission_test.go`
- `internal/cluster/rpc/tls_test.go`
- `docs/architecture/phase-8.7-recovery-report.md`

### 删除

- 无。

## 数据与 Git 安全

```text
DOCKER: PASS (isolated resources only)
DATA SAFETY: PASS
BRANCH: main
HEAD: 0c349f0c7b9babd62dabf8d7cd01c107d4e69621
COMMIT: NO
PUSH: NO
NEXT PHASE: 8.8
```
