# Plan — Phase 2: Runtime + Built-in Cells

## 实施策略

5 波渐进式实施，每波有明确的入口/出口条件。

---

## Wave 0: 前置准备（阻塞后续所有 Wave）

### 0.1 YAML 元数据修正

修正 S2 审查发现的元数据遗漏，确保 `gocell validate` 零 error：

| 修正项 | 文件 | 变更 |
|--------|------|------|
| audit-append subscribe 声明 | `cells/audit-core/slices/audit-append/slice.yaml` | 新增 6 个 event contract 的 subscribe role |
| config-subscribe subscribe 声明 | `cells/config-core/slices/config-subscribe/slice.yaml` | 新增 event.config.changed.v1 subscribe role |
| http.auth.me.v1 serving slice | `cells/access-core/slices/identity-manage/slice.yaml` | 新增 http.auth.me.v1 serve role |
| event contract subscribers 更新 | 多个 contract.yaml | event.config.changed.v1 加 audit-core；event.session/user.* 确认含 audit-core |
| session-refresh contract | 新建 `contracts/http/auth/refresh/v1/contract.yaml` | http.auth.refresh.v1, ownerCell=access-core |
| session-refresh slice 更新 | `cells/access-core/slices/session-refresh/slice.yaml` | 新增 http.auth.refresh.v1 serve role |
| J-session-refresh 更新 | `journeys/J-session-refresh.yaml` | contracts 改为 http.auth.refresh.v1 |
| J-sso-login OIDC criteria | `journeys/J-sso-login.yaml` | OIDC passCriteria mode 改为 manual |

### 0.2 kernel/ 接口扩展

| 文件 | 变更 |
|------|------|
| `kernel/outbox/outbox.go` | 新增 `Subscriber` 接口 |
| `kernel/outbox/outbox_test.go` | 接口合规性测试 |
| `kernel/cell/interfaces.go` | 新增 `HTTPRegistrar` / `EventRegistrar` / `RouteMux` 可选接口 |
| `kernel/cell/interfaces_test.go` | 类型断言测试 |

### 0.3 CLAUDE.md 更新

补充依赖规则：`runtime/ 可依赖 kernel/ 和 pkg/`

### 0.4 验证

```bash
go build ./... && go test ./... && gocell validate
```

**出口条件**: gocell validate 零 error + go build 通过 + kernel/ 测试绿色

---

## Wave 1: runtime/ 独立模块（可全部并行）

### 1.1 runtime/http/middleware (7 文件)

每个中间件: `{name}.go` + `{name}_test.go`

对标拉取：
- Kratos `go-kratos/kratos/middleware/`
- go-zero `zeromicro/go-zero/rest/handler/`

### 1.2 runtime/config

`config.go` + `watcher.go` + `config_test.go` + `watcher_test.go`

对标拉取：go-micro `config/`

### 1.3 runtime/shutdown

`shutdown.go` + `shutdown_test.go`

### 1.4 runtime/worker

`worker.go` + `periodic.go` + `worker_test.go`

对标拉取：go-zero `core/service/servicegroup.go`

### 1.5 runtime/auth

抽象接口: `TokenVerifier` / `Authorizer` + 中间件封装
`auth.go`(接口定义) + `middleware.go` + `servicetoken.go` + tests

对标拉取：Kratos `middleware/auth/`

### 1.6 runtime/observability

- `metrics/metrics.go` + test
- `tracing/tracing.go` + test (Phase 2: stdout exporter)
- `logging/logging.go` + test

对标拉取：Kratos `middleware/tracing/`, `middleware/metrics/`

### 1.7 runtime/eventbus

`eventbus.go` + `eventbus_test.go`
实现 `outbox.Publisher` + `outbox.Subscriber`
at-most-once + 3 次重试 + dead letter channel

对标拉取：Watermill `message/`

**出口条件**: 每个模块编译通过 + 单元测试绿色 + 覆盖率 >= 80%

---

## Wave 2: runtime/ 集成模块 + Cell domain/ports

### 2.1 runtime/http/health

`health.go` + `health_test.go`
依赖: `kernel/assembly.CoreAssembly.Health()`

### 2.2 runtime/http/router

`router.go` + `router_test.go`
依赖: middleware + health + chi

### 2.3 runtime/bootstrap

`bootstrap.go` + `bootstrap_test.go`
依赖: config + router + worker + shutdown + eventbus + assembly

对标拉取：Uber fx `app.go`、Kratos `app.go`

### 2.4 Cell domain + ports（与 2.1-2.3 并行）

不依赖 HTTP/router，可提前开发：

- `cells/access-core/internal/domain/` — user.go, session.go, role.go
- `cells/access-core/internal/ports/` — user_repo.go, session_repo.go, role_repo.go
- `cells/audit-core/internal/domain/` — entry.go, hashchain.go
- `cells/audit-core/internal/ports/` — audit_repo.go, archive_store.go
- `cells/config-core/internal/domain/` — config_entry.go, version.go, feature_flag.go
- `cells/config-core/internal/ports/` — config_repo.go, flag_repo.go

每个 domain 模型含 table-driven 单元测试。

**出口条件**: bootstrap 可编排 Assembly + HTTP + Worker 启动/关闭 + Cell domain 模型编译通过

---

## Wave 3: Cell 完整实现（3 个 Cell 串行，按优先级排序）

### 3.1 config-core（首个，验证注入模式）

作为参考实现验证 Slice 构造时注入模式：

1. `cell.go` — ConfigCore 实现 Cell + HTTPRegistrar + EventRegistrar
2. 5 slices 各 handler.go + service.go + service_test.go
3. in-memory ConfigRepository + FlagRepository 桩实现
4. 生命周期集成测试

### 3.2 access-core

1. `cell.go` — AccessCore 实现 Cell + HTTPRegistrar + EventRegistrar
2. session-validate 提供 TokenVerifier 实现
3. authorization-decide 提供 Authorizer 实现
4. 7 slices 各 handler + service + test
5. in-memory UserRepo / SessionRepo / RoleRepo 桩
6. JWT RS256 签发/验证（使用 golang-jwt/jwt/v5）

### 3.3 audit-core

1. `cell.go` — AuditCore 实现 Cell + EventRegistrar
2. HMAC-SHA256 hash chain 实现
3. 4 slices handler + service + test (audit-archive = stub)
4. 注册 6 个事件订阅
5. in-memory AuditRepository 桩

**出口条件**: 3 个 Cell 各自编译通过 + 单元测试绿色 + 覆盖率 >= 80%

---

## Wave 4: 集成 + 验证

### 4.1 cmd/core-bundle 更新

更新 `cmd/core-bundle/main.go` 使用 Bootstrap 编排 3 个 Cell（硬编码注册顺序: config-core → access-core → audit-core）

### 4.2 Journey 端到端验证

- Hard Gate (5 条): J-sso-login / J-session-refresh / J-session-logout / J-user-onboarding / J-account-lockout
- Soft Gate (3 条): J-audit-login-trail / J-config-hot-reload / J-config-rollback

### 4.3 文档

- runtime/ 核心 package doc.go (http/middleware, config, bootstrap)
- Cell 开发指南
- README.md 更新

### 4.4 DevOps

- Makefile (build / test / validate / generate)

**出口条件**: go build + go test + gocell validate + 5 Hard Gate journey + 3 Soft Gate journey

---

## 数据模型

### access-core domain

```
User { ID, Username, Email, PasswordHash, Status(active/locked), CreatedAt, UpdatedAt }
Session { ID, UserID, AccessToken, RefreshToken, ExpiresAt, RevokedAt, CreatedAt }
Role { ID, Name, Permissions []Permission }
Permission { Resource, Action }
```

### audit-core domain

```
AuditEntry { ID, EventID, EventType, ActorID, Timestamp, Payload, PrevHash, Hash }
HashChain { Entries []AuditEntry, HMACKey []byte }
  - Append(entry) → 计算 HMAC-SHA256(prevHash + entry)
  - Verify(from, to) → 逐条校验 hash 链
```

### config-core domain

```
ConfigEntry { ID, Key, Value, Version, CreatedAt, UpdatedAt }
ConfigVersion { ID, ConfigID, Version, Value, PublishedAt }
FeatureFlag { ID, Key, Type(boolean/percentage), Value, RolloutPercentage, Enabled }
  - Evaluate(subject) → bool (percentage: hash(subject+key) % 100 < percentage)
```
