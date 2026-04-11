# Spec — Phase 3: Adapters

> 分支: `feat/002-phase3-adapters`
> 输入: phase-charter.md, product-context.md, roadmap Phase 3, Phase 2 tech-debt.md (80 条)

---

## 1. 概述

Phase 3 实现 6 个外部系统适配器（postgres, redis, oidc, s3, rabbitmq, websocket），将 Phase 2 的 in-memory 实现替换为真实基础设施。同时系统性偿还 Phase 2 的 80 条 tech-debt，完成安全加固和测试补全。

交付后 GoCell 从 "可运行但仅有 in-memory 实现" 升级为 "可连接真实基础设施的集成就绪框架"。

---

## 2. 功能需求 (FR)

### FR-1: PostgreSQL Adapter (`adapters/postgres/`)

系统必须提供基于 `pgx/v5` 的 PostgreSQL 适配器，包含：

| 子模块 | 说明 |
|--------|------|
| FR-1.1 连接池 | `Pool` struct，支持 DSN/环境变量配置，连接池大小、idle timeout、max lifetime 可配，提供 `Health() error` 方法 |
| FR-1.2 TxManager | `TxManager` struct，实现 `RunInTx(ctx, func(tx pgx.Tx) error) error`，支持嵌套调用（savepoint）、超时控制、panic 回滚 |
| FR-1.3 Migrator | `Migrator` struct，从 `embed.FS` 加载 SQL migration 文件，支持 up/down/status，维护 `schema_migrations` 表 |
| FR-1.4 Outbox Writer | 实现 `kernel/outbox.Writer` 接口，使用 context-embedded transaction 模式：`TxManager.RunInTx` 将 `pgx.Tx` 存入 context，`OutboxWriter.Write` 从 context 提取 tx 在同一事务内写入 `outbox_entries` 表。若 context 无 tx 则 fail-fast 返回 `ERR_ADAPTER_NO_TX`。（决策 1） |
| FR-1.5 Outbox Relay | 实现 `kernel/outbox.Relay` + `worker.Worker` 接口（决策 KS-07），轮询 `outbox_entries` 表中未发布条目，调用 `outbox.Publisher`（kernel 接口，非具体 adapter 类型）后标记已发布。Relay 将完整 `Entry`（含 ID, AggregateID, Metadata）序列化为 JSON 作为 payload（决策 9）。策略：`SELECT ... FOR UPDATE SKIP LOCKED` 支持多实例并发；默认 poll interval 1s，batch size 100（均可通过 `RelayConfig` 配置）；已发布条目保留 72h，由 `PeriodicWorker` 清理。Relay 通过 `bootstrap.WithWorkers()` 注册参与生命周期管理 |
| FR-1.6 Repository 基础设施 | `adapters/postgres` 提供 `RowScanner`（pgx 专用）；`pkg/query` 提供通用 `Builder` 辅助类型，降低 Cell Repository 实现的样板代码。不实现具体 Cell Repository（由各 Cell 自行实现） |

**对标参考**: Watermill `watermill-sql` outbox 模式 + pgx/v5 标准用法。

### FR-2: Redis Adapter (`adapters/redis/`)

系统必须提供基于 `go-redis/v9` 的 Redis 适配器，包含：

| 子模块 | 说明 |
|--------|------|
| FR-2.1 连接 | `Client` struct，支持 standalone/sentinel 配置，提供 `Health() error`、`Close() error` |
| FR-2.2 分布式锁 | `DistLock` struct，实现 Redlock 风格的分布式锁，支持 TTL、续租、解锁。提供 `Acquire(ctx, key, ttl) (Lock, error)` + `Lock.Release(ctx) error` |
| FR-2.3 Idempotency Checker | 实现 `kernel/idempotency.Checker` 接口，使用 Redis `SET NX` + TTL 实现 `IsProcessed(ctx, key) (bool, error)` + `MarkProcessed(ctx, key, ttl) error` |
| FR-2.4 Cache | `Cache` struct，提供 `Get/Set/Delete` + TTL，支持 JSON 序列化/反序列化泛型 helper |

### FR-3: OIDC Adapter (`adapters/oidc/`) — thin go-oidc v3 wrapper

> **PR#41 重构**: 删除自建类型（DiscoveryDocument/IDTokenClaims/TokenResponse/UserInfo），
> 改为暴露 `coreos/go-oidc` 和 `golang.org/x/oauth2` 原生类型。
> ExchangeCode/GetUserInfo 由调用方通过 `OAuth2Config()` 和 go-oidc Provider 直接完成。

| 子模块 | 说明 |
|--------|------|
| FR-3.1 Provider | `Adapter.Provider(ctx)` — 懒初始化 go-oidc Provider（含 OIDC Discovery） |
| FR-3.2 Refresh | `Adapter.Refresh(ctx)` — 强制重新 discovery（JWKS/metadata 轮转） |
| FR-3.3 Verifier | `Adapter.Verifier(ctx)` — 返回 go-oidc `IDTokenVerifier` |
| FR-3.4 OAuth2Config | `Adapter.OAuth2Config(ctx)` — 返回 `oauth2.Config`，调用方直接用于 token exchange |
| ~~FR-3.5~~ | **已移除**: ExchangeCode/UserInfo 不再由 adapter 包装，调用方直接用 go-oidc + oauth2 |

### FR-4: S3 Adapter (`adapters/s3/`) — thin aws-sdk-go-v2 wrapper

> **PR#41 重构**: 精简为 Upload + Health + SDK 逃生口。
> Download/Delete/PresignedURL 由调用方通过 `client.SDK()` 直接使用 aws-sdk-go-v2 完成。

| 子模块 | 说明 |
|--------|------|
| FR-4.1 Client | `New(cfg) (*Client, error)` — aws-sdk-go-v2 S3 client，支持 MinIO（UsePathStyle） |
| FR-4.2 Upload | `Upload(ctx, key, data, contentType) error` — 实现 ObjectUploader 接口 |
| FR-4.3 Health | `Health(ctx) error` — HeadBucket 检查 |
| FR-4.4 SDK | `SDK() *s3.Client` — 暴露底层 SDK client 用于 download/delete/presigned 等高级操作 |
| ~~FR-4.5~~ | **已移除**: Download/Delete/PresignedURL 不再由 adapter 包装 |

### FR-5: RabbitMQ Adapter (`adapters/rabbitmq/`)

系统必须提供基于 `amqp091-go` 的 RabbitMQ 适配器，包含：

| 子模块 | 说明 |
|--------|------|
| FR-5.1 连接管理 | `Connection` struct，支持 AMQP URL 配置、自动重连（exponential backoff）、channel 池、`Health() error` |
| FR-5.2 Publisher | 实现 `kernel/outbox.Publisher` 接口，`Publish(ctx, topic, payload) error`。支持 confirm mode（publisher confirms）、mandatory flag |
| FR-5.3 Subscriber | 实现 `kernel/outbox.Subscriber` 接口，`Subscribe(ctx, topic, handler) error`。`SubscriberConfig` 必须设置 `DLXExchange`（运行时必填，防止 Nack(requeue=false) 静默丢消息）。Consumer group 和 prefetch count 通过 `SubscriberConfig` 构造函数注入（`NewSubscriber(conn, cfg SubscriberConfig)`），不扩展 kernel 接口签名（决策 PM-08） |
| FR-5.4 ConsumerBase | `ConsumerBase` struct，封装两阶段幂等（`idempotency.Claimer` Claim/Commit/Release）、自动重试（exponential backoff）、死信路由（DLX exchange）。遵循 eventbus.md 规范：`DispositionAck` → broker Ack → Receipt.Commit；`DispositionRequeue` → 退避重试；`DispositionReject` / `PermanentError` → broker Nack(requeue=false) → DLX |
| FR-5.5 DLQ 可观测 | 死信消息必须有 slog 日志记录（event_id、topic、error、retry_count）+ 死信计数 metric（或 slog 计数日志） |

**对标参考**: Watermill `watermill-amqp` subscriber/publisher 模式。

### FR-6: WebSocket Adapter (`adapters/websocket/`)

系统必须提供基于 `nhooyr.io/websocket` 的 WebSocket 适配器，包含：

| 子模块 | 说明 |
|--------|------|
| FR-6.1 Hub | `Hub` struct，管理 WebSocket 连接（注册/注销/广播/单播）。连接以 `connectionID + userID` 标识 |
| FR-6.2 Signal-First 模式 | Hub 默认推送轻量信号（`{"type":"refresh","resource":"config"}`），客户端收到信号后主动拉取最新数据。不通过 WebSocket 推送完整数据 payload |
| FR-6.3 HTTP 升级 | 提供 `UpgradeHandler(hub *Hub) http.Handler`，处理 WebSocket 握手。支持 origin 检查、子协议协商 |
| FR-6.4 心跳 | 定时 ping/pong，超时自动断开。可配置间隔和超时 |

### FR-7: Docker Compose 集成环境

系统必须提供 Docker Compose 配置用于开发和集成测试：

| 子模块 | 说明 |
|--------|------|
| FR-7.1 服务定义 | PostgreSQL 15+、Redis 7+、RabbitMQ 3.12+（含 management plugin）、MinIO（S3 兼容）|
| FR-7.2 健康检查 | 每个服务必须有 healthcheck，`docker compose up -d --wait` 30 秒内全部 healthy。`make test-integration` 脚本包含带超时的健康检查等待步骤，超 30s exit 1（决策 PM-07） |
| FR-7.3 数据卷 | 开发用 named volume 持久化数据，CI 用 tmpfs 加速 |
| FR-7.4 环境变量 | `.env.example` 定义所有 adapter 连接参数的默认值 |

### FR-8: Testcontainers 集成测试

系统必须提供基于 `testcontainers-go` 的集成测试：

| 子模块 | 说明 |
|--------|------|
| FR-8.1 每 adapter 独立测试 | 每个 adapter 包至少 1 个 `_integration_test.go` 文件，使用 `//go:build integration` tag |
| FR-8.2 Outbox 全链路测试 | 覆盖: 业务写入 + outbox 写入（同事务） → Relay 轮询 → RabbitMQ publish → Consumer 消费 → Idempotency 去重 |
| FR-8.3 DLQ 测试 | 覆盖: 消费失败 → 3x 重试 → 路由到 DLQ → DLQ 可读取 |
| FR-8.4 Journey 集成测试 | J-audit-login-trail（login → audit event → audit 写入 DB → hash chain 验证）、J-config-hot-reload（config 变更 → RabbitMQ event → subscriber 重载）、J-config-rollback（config rollback → event → subscriber 重载为旧版本 → DB 验证）端到端验证。前置依赖：AuditRepository + ConfigRepository PG 实现（决策 4） |
| FR-8.5 Assembly 组合集成测试 | 至少 1 个 testcontainers 测试验证 postgres Pool + TxManager + OutboxWriter + rabbitmq Publisher + redis IdempotencyChecker 同时注入 CoreAssembly，执行 Start → 业务写入 → outbox relay → consume → idempotency → Stop 全生命周期（决策 11） |

### FR-9: Phase 2 安全加固

系统必须修复 Phase 2 遗留的 8 条安全类 tech-debt：

| 编号 | 问题 | 修复要求 |
|------|------|---------|
| FR-9.1 SEC-03 | 密钥硬编码 | 改为环境变量读取 + 启动缺失时 fail-fast。**AC**: Given `GOCELL_JWT_PRIVATE_KEY` 未设置; When 服务启动; Then 5s 内退出，slog Error 含 "missing required config" |
| FR-9.2 SEC-04 | JWT HS256 | 迁移至 RS256，Cell.Init 注入公私钥对。**AC**: Given RS256 token; When 验证; Then PASS。Given HS256 token; When 验证; Then 401 |
| FR-9.3 SEC-06 | RealIP 无条件信任 XFF | 新增 `trustedProxies []string` 配置。**AC**: Given XFF from non-trusted IP; When 请求; Then RealIP 使用 RemoteAddr 而非 XFF |
| FR-9.4 SEC-07 | ServiceToken HMAC 无 timestamp | 签名加入 timestamp + 5 分钟窗口（含边界）。**AC**: Given timestamp 5m01s ago; When 验证; Then 拒绝 |
| FR-9.5 SEC-08 | ID 用 UnixNano | 7 处改为 `crypto/rand` UUID。**AC**: 100 次并发调用生成 100 个不同 ID |
| FR-9.6 SEC-09 | refresh token 未检查 signing method | 显式校验 `token.Method == jwt.SigningMethodRS256`。**AC**: Given token with alg=none; When 验证; Then 拒绝 |
| FR-9.7 SEC-10 | refresh token 无 rotation reuse detection | refresh 成功后旧 token 失效，复用旧 token 吊销该 session 所有 token。**AC**: Given refresh_v1 已换 refresh_v2; When 重放 refresh_v1; Then 拒绝 + session 所有 token 失效 |
| FR-9.8 SEC-11 | API 端点无认证 | 公开端点: `/healthz`, `/readyz`, `/api/v1/auth/login`, `/api/v1/auth/callback`; 其余为保护端点，加装 JWT 中间件。**AC**: Given 无 token; When GET /api/v1/users; Then 401 |

### FR-10: Phase 2 Tech-Debt 偿还

系统必须系统性处理 Phase 2 tech-debt.md 中的 80 条债务：

| 子模块 | 说明 |
|--------|------|
| FR-10.1 编码规范 | kernel/slice/verify.go 7 处 fmt.Errorf → errcode (#27); cells/ 15 处 fmt.Errorf → errcode.Wrap (#79); eventbus "bus is closed" → errcode (#73) |
| FR-10.2 架构修复 | ARCH-04 BaseSlice 空壳评估重构; ARCH-06 goroutine context 改用 shutdownCtx; ARCH-07 L2 事件改 outbox 事务 |
| FR-10.3 生命周期修复 | shutdown.Manager LIFO 顺序 (#71); Worker.Stop 改串行反序 (#74); Assembly.Stop 状态机守卫 (#19); BaseCell 线程安全 (#50) |
| FR-10.4 测试补全 | handler 层 httptest 补全达 >= 80% (#13/#32); bootstrap.go 覆盖率提升 (#15); cmd/core-bundle 冒烟测试 (#18); router.go 覆盖率 >= 80% (#P2) |
| FR-10.5 治理规则 | VERIFY-01 扩展到所有角色 (#28); FMT projection replayable 校验 (#29); 禁用字段名检测 (#41); Parser 空 id 校验 (#44) |
| FR-10.6 运维/DX | config watcher 集成 bootstrap (#20); eventbus 健康暴露 (#21); doc.go 补全 (#22); TopicConfigChanged 常量统一 (#23) |
| FR-10.7 边界修复 | #60 configsubscribe unmarshal 失败 → DLQ 路由（依赖 FR-5.4）; #61 auditappend publish → outbox.Writer 事务内写入（依赖 FR-1.4）（决策 8） |

**验证方式**（决策 PM-03）:
- FR-10.1: `grep -rn "fmt.Errorf" src/kernel/ src/cells/` 对外暴露路径返回 0 匹配
- FR-10.3: shutdown 顺序 table-driven 单元测试
- FR-10.4: handler >= 80%, bootstrap >= 70%, router >= 80%
- **S7 计数规则**: 80 条中 DEFERRED 6 条（#54, #56-59, #62）不计入分母，有效分母 74 条，至少 60 条 RESOLVED。完整条目清单见 `specs/feat/001-phase2-runtime-cells/tech-debt.md`

### FR-11: Phase 2 产品修复

| 编号 | 问题 | 修复要求 |
|------|------|---------|
| FR-11.1 | 审计查询 time.Parse 静默忽略 | 返回 400 + `ERR_VALIDATION_INVALID_TIME_FORMAT` |
| FR-11.2 | RateLimit Retry-After 硬编码 1 秒 | 扩展 RateLimiter 配置，Retry-After 基于实际限流窗口计算 |
| FR-11.3 | Update user 仅支持 email | `PATCH /api/v1/users/{id}`，JSON merge patch（RFC 7396）语义：缺失字段不更新。可更新字段: `name`(string), `email`(string, 唯一), `status`(enum: active/suspended)。**AC**: Given user status=active; When PATCH `{"name":"new"}`; Then 仅 name 更新，email/status 不变。现有仅发 email 的客户端行为不变（决策 PM-09） |
| FR-11.4 | AC-8.2 签名算法文档对齐 | 修订 product-acceptance-criteria.md，HS256 → RS256 记录迁移历史 |

### FR-12: 文档需求

系统必须提供以下文档产出：

| 编号 | 文档 | 说明 |
|------|------|------|
| FR-12.1 | adapter godoc | 6 个 adapter 包均有 `doc.go`，每个导出类型/函数有注释 |
| FR-12.2 | runtime doc.go 补全 | Phase 2 遗留 11 个 runtime 包补全 `doc.go` (#22) |
| FR-12.3 | 集成测试指南 | 如何运行 `docker compose up` + `go test -tags=integration` |
| FR-12.4 | adapter 配置参考 | 每个 adapter 的环境变量 / YAML 配置项说明 |
| FR-12.5 | Cell 开发指南更新 | 补充 contract test 编写指引和错误处理模式说明 |

### FR-13: DevOps 需求

系统必须更新构建和部署配置：

| 编号 | 配置 | 说明 |
|------|------|------|
| FR-13.1 | Docker Compose | `docker-compose.yml` + `.env.example`（FR-7 详述） |
| FR-13.2 | Makefile/脚本 | `make test-integration` — 启动 Docker Compose + 运行 `go test -tags=integration` + 清理 |
| FR-13.3 | go.mod 依赖 | 新增 5 个直接依赖: `pgx/v5`, `go-redis/v9`, `amqp091-go`, `nhooyr.io/websocket`, `testcontainers-go` |
| FR-13.4 | SQL Migrations | `adapters/postgres/migrations/` 下的初始 migration（outbox_entries 表、schema_migrations 表）|

### FR-14: 测试需求

系统必须提供以下测试覆盖：

| 编号 | 测试类型 | 说明 |
|------|---------|------|
| FR-14.1 | 单元测试 | 每个 adapter 包有 `_test.go`，mock 外部依赖，覆盖配置解析、错误路径、边界条件 |
| FR-14.2 | 集成测试 | `//go:build integration` tag，testcontainers 驱动，覆盖 FR-8 全部场景 |
| FR-14.3 | Journey 端到端 | J-audit-login-trail + J-config-hot-reload + J-config-rollback 真实端到端（FR-8.4） |
| FR-14.4 | 回归测试 | Phase 2 全量 `go test ./...` 不退化，kernel/ 覆盖率 >= 90% |

### FR-15: Bootstrap 重构（Wave 0 前置）

系统必须重构 `runtime/bootstrap` 以支持接口注入（决策 3）：

| 子模块 | 说明 |
|--------|------|
| FR-15.1 接口化 | 新增 `WithPublisher(outbox.Publisher)` + `WithSubscriber(outbox.Subscriber)` Option，替代 `WithEventBus(*eventbus.InMemoryEventBus)` 的具体类型绑定 |
| FR-15.2 向后兼容 | 保留 `WithEventBus` 作为便利方法，内部调用 WithPublisher + WithSubscriber |
| FR-15.3 kernel doc | `outbox.Writer.Write` godoc 增加 context-embedded transaction 约定说明；`outbox.Entry.ID` godoc 标注为 canonical idempotency identifier |

---

## 3. 非功能需求 (NFR)

### NFR-1: 分层隔离

- adapters/ 仅 import kernel/ + runtime/ + pkg/ + 外部依赖
- adapters/ 不 import cells/（Cell Repository 实现由 Cell 自行提供，adapter 仅提供基础设施）
- kernel/ 不 import adapters/
- runtime/ 不 import adapters/
- `go build ./...` 编译通过即验证

### NFR-2: 接口契约

- 每个 adapter 实现的 kernel 接口必须通过接口赋值检查（`var _ outbox.Writer = (*PostgresOutboxWriter)(nil)`）
- 接口实现不得扩展 kernel 接口签名（adapter 可有额外方法，但接口方法签名不变）

### NFR-3: 覆盖率

- adapters/ 每包 >= 80%（单元测试 + 集成测试合计）
- kernel/ 每包 >= 90%（维持 Phase 2 水平）
- cells/ Cell 级聚合 >= 80%（Phase 2 水平维持或提升）
- runtime/ 每包 >= 80%（修复 Phase 2 遗留 bootstrap 51.4% + router 78.8%）

### NFR-4: 外部依赖可控

- Phase 3 新增直接依赖限 5 个: `pgx/v5`, `go-redis/v9`, `amqp091-go`, `nhooyr.io/websocket`, `testcontainers-go`
- 不引入白名单外直接依赖
- testcontainers-go 仅用于 test（`//go:build integration`），不进入生产二进制

### NFR-5: 连接韧性

- 每个 adapter 必须支持连接失败后自动重连（或明确文档说明不支持的原因）
- 提供 `Health() error` 方法，集成 `runtime/http/health/` 健康检查端点

### NFR-6: 配置可注入

- 每个 adapter 的配置通过 struct（`Config`）注入，支持从环境变量读取
- 不硬编码连接参数
- 提供合理默认值（连接池大小、超时、重试间隔）

### NFR-7: 可观测性

- adapter 操作使用 `slog` 结构化日志（连接、重连、错误、慢查询）
- 遵循 observability.md 日志级别规范（Error: DB 写入失败; Warn: 重连; Info: 连接建立; Debug: 查询详情）
- RabbitMQ DLQ 消息必须可观测（FR-5.5）

### NFR-8: 优雅关闭

- 每个 adapter 提供 `Close(ctx context.Context) error`
- 完整关闭顺序（决策 ARCH-08）：1. Stop Subscribers → 2. Stop Relay（drain 当前 batch） → 3. Stop Publisher（flush confirms） → 4. Close RabbitMQ → 5. Close PostgreSQL → 6. Close Redis
- 超时控制：ctx 取消后强制关闭

---

## 4. 架构约束

### 4.1 依赖方向

```
kernel/outbox.Writer       ←── adapters/postgres/outbox_writer.go
kernel/outbox.Relay        ←── adapters/postgres/outbox_relay.go
kernel/outbox.Publisher    ←── adapters/rabbitmq/publisher.go
kernel/outbox.Subscriber   ←── adapters/rabbitmq/subscriber.go
kernel/idempotency.Checker ←── adapters/redis/idempotency.go
```

adapters/ 实现 kernel/ 定义的接口，不反向依赖。adapters/ 不 import cells/（决策 2）。ArchiveStore 由 `cells/audit-core/internal/adapters/s3archive/` 实现。

### 4.2 注入点

adapter 实例在 assembly 层（`cmd/core-bundle/main.go`）创建并注入到 Cell 的 `Dependencies`：

```go
pool := postgres.NewPool(cfg.Postgres)
txMgr := postgres.NewTxManager(pool)
outboxWriter := postgres.NewOutboxWriter(pool)
outboxRelay := postgres.NewOutboxRelay(pool, rabbitPublisher) // rabbitPublisher 作为 outbox.Publisher 接口注入
redisClient := redis.NewClient(cfg.Redis)
idempChecker := redis.NewIdempotencyChecker(redisClient)
```

### 4.3 错误处理

- adapter 层错误用 `pkg/errcode` 包装，错误码前缀 `ERR_ADAPTER_*`
- 底层驱动错误必须 wrap context：`errcode.Wrap(ERR_ADAPTER_PG_QUERY, "query users", err)`
- 不暴露底层驱动的原始错误消息给上层

**Adapter 错误码前缀清单**（决策 PM-04）:

| adapter | 前缀 | 典型错误码 |
|---------|------|-----------|
| postgres | `ERR_ADAPTER_PG_*` | `_CONNECT`, `_QUERY`, `_TX_TIMEOUT`, `_NO_TX` |
| redis | `ERR_ADAPTER_REDIS_*` | `_CONNECT`, `_LOCK_ACQUIRED`, `_LOCK_TIMEOUT` |
| rabbitmq | `ERR_ADAPTER_AMQP_*` | `_CONNECT`, `_CONNECT_PERMANENT`, `_PUBLISH`, `_CONFIRM_TIMEOUT`, `_SUBSCRIBE`, `_CONSUME`, `_RECONNECT_EXHAUSTED` |
| oidc | `ERR_ADAPTER_OIDC_*` | `_DISCOVERY`, `_TOKEN_VERIFY`, `_TOKEN_EXCHANGE` |
| s3 | `ERR_ADAPTER_S3_*` | `_UPLOAD`, `_DOWNLOAD`, `_NOT_FOUND` |
| websocket | `ERR_ADAPTER_WS_*` | `_UPGRADE`, `_SEND`, `_CLOSED` |

### 4.4 对标参考

| 模块 | Primary 对标 | Secondary 对标 |
|------|-------------|---------------|
| adapters/postgres (outbox) | Watermill `watermill-sql` | — |
| adapters/rabbitmq | Watermill `watermill-amqp` | — |
| adapters/redis | go-micro `store/redis` | — |
| adapters/oidc | coreos/go-oidc | — |
| adapters/s3 | minio/minio-go | — |
| adapters/websocket | nhooyr.io/websocket examples | — |

---

## 5. 接口清单

### 5.1 已定义接口（kernel 层，Phase 3 实现）

```go
// kernel/outbox
type Writer interface {
    Write(ctx context.Context, entry Entry) error
}
type Relay interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
type Publisher interface {
    Publish(ctx context.Context, topic string, payload []byte) error
}
type Subscriber interface {
    Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error
    Close() error
}

// kernel/idempotency
type Checker interface {
    IsProcessed(ctx context.Context, key string) (bool, error)
    MarkProcessed(ctx context.Context, key string, ttl time.Duration) error
}
```

### 5.2 Cell Repository 接口（由各 Cell 自行实现 adapter 层）

```go
// cells/access-core/internal/ports
type UserRepository interface { ... }
type SessionRepository interface { ... }
type RoleRepository interface { ... }

// cells/audit-core/internal/ports
type AuditRepository interface { ... }
type ArchiveStore interface { ... }

// cells/config-core/internal/ports
type ConfigRepository interface { ... }
type FlagRepository interface { ... }
```

Phase 3 提供 `adapters/postgres/` 基础设施（Pool, TxManager, RowScanner）。具体 Repository 实现由 Cell 内部 `internal/adapters/` 子包完成：
- **Phase 3 实现**（决策 4）: `cells/audit-core/internal/adapters/postgres/audit_repo.go` + `cells/config-core/internal/adapters/postgres/config_repo.go` — J-audit-login-trail 和 J-config-hot-reload 端到端测试前置
- **Phase 4 实现**: UserRepository, SessionRepository, RoleRepository, FlagRepository, ArchiveStore (S3 wrapper)

---

## 6. 范围排除

- 不实现 OIDC provider 服务端能力（仅 client）
- 不引入消息 Schema Registry（JSON + 版本化 topic）
- 不实现生产级 Kubernetes 配置
- 不实现性能基准测试
- VictoriaMetrics adapter 延迟至 Phase 4（Phase 3 聚焦持久化和消息传递，指标推送优先级低）（决策 5）
- Grafana dashboard 模板延迟至 Phase 4（依赖指标端点稳定）（决策 RM-08）
- 不使用 `adapters/family/` 子目录（所有 6 adapter 扁平放在 `adapters/`，对 master-plan Layer 4/5 的有意偏离）（决策 7）
- Phase 2 DEFERRED 的 6 条高风险重构（#54 TOCTOU, #56-59 domain 模型, #62 rollback version）不在本 Phase 范围。#60/#61 已纳入 FR-10.7（决策 8）

---

## 7. 风险与缓解

| 风险 | 影响 | 缓解 |
|------|------|------|
| testcontainers 在 CI 环境不可用 | 集成测试无法自动化 | 提供 `docker compose` 降级方案；集成测试有 `//go:build integration` tag，默认不运行 |
| pgx/v5 + go 1.25 兼容性 | 编译失败 | Phase 3 开始前验证 `go get` 可正常拉取 |
| 80 条 tech-debt 处理量过大 | 进度风险 | 按 P0/P1/P2/P3 分层，P0 安全类优先；P2/P3 溢出则 DEFERRED |
| RabbitMQ 自动重连复杂度 | 消费中断 | 参考 Watermill amqp reconnect 实现，优先保证 at-least-once 语义 |
| JWT RS256 迁移向后不兼容 | 已发放 HS256 token 失效 | Phase 2 无生产 token，直接切换无兼容负担 |
| 72 条 tech-debt 挤压 adapter 进度 | adapter 质量受损 | Wave 1 优先 adapter 核心，Wave 3-4 P2/P3 tech-debt 允许 DEFERRED 溢出 |

---

## 8. 交付波次（决策 6）

```
Wave 0: 前置准备（无外部依赖）
  - FR-15: Bootstrap 接口化重构（KS-06 blocker）
  - kernel/outbox doc 增强（KS-01, KS-03）
  - FR-4.4 移除确认 + S3 ObjectStore 设计（KS-10）
  - 安全前置: FR-9.5 UUID 替换（不依赖 adapter）

Wave 1: 基础设施 adapter（需外部依赖 + Docker）
  - FR-1.1~1.3: postgres Pool, TxManager, Migrator
  - FR-2.1~2.4: redis Client, DistLock, IdempotencyChecker, Cache
  - FR-5.1~5.5: rabbitmq Connection, Publisher, Subscriber, ConsumerBase, DLQ
  - FR-7 + FR-13.1~13.4: Docker Compose + Makefile + SQL migrations

Wave 2: 应用 adapter + 集成接线
  - FR-1.4~1.6: postgres OutboxWriter, OutboxRelay, Repo 基础设施
  - FR-3: oidc Provider, TokenExchange, JWKS
  - FR-4.1~4.3: s3 Client, 对象操作, Presigned URL
  - FR-6: websocket Hub, UpgradeHandler, 心跳

Wave 3: Cell 集成 + 安全 + tech-debt
  - FR-10.2 ARCH-07: 7 处 Cell Publish → outbox.Writer（KS-05）
  - 决策 4: AuditRepository + ConfigRepository PG 实现
  - FR-9: 安全加固 8 条（除 FR-9.5 已在 Wave 0）
  - FR-10.1/10.3/10.5/10.6/10.7: tech-debt P0 + P1
  - FR-11: 产品修复

Wave 4: 测试 + 文档
  - FR-8: 全部 testcontainers 集成测试
  - FR-14: 单元测试补全 + 回归
  - FR-10.4: handler/bootstrap/router 覆盖率补全
  - FR-12: godoc, doc.go, 指南, 配置参考
```

Wave 1 是 Phase 3 Gate 硬性前提。Wave 3-4 中 P2-Low tech-debt 允许溢出至 Phase 4。

---

## 附录 A: 配置默认值参考（决策 PM-05）

| adapter | 参数 | 默认值 |
|---------|------|--------|
| postgres | pool max conns | 10 |
| postgres | idle timeout | 5m |
| postgres | max conn lifetime | 1h |
| postgres | outbox relay poll interval | 1s |
| postgres | outbox relay batch size | 100 |
| postgres | outbox entry retention | 72h |
| redis | dial timeout | 5s |
| redis | read timeout | 3s |
| redis | dist lock default TTL | 30s |
| rabbitmq | reconnect max backoff | 30s |
| rabbitmq | prefetch count | 10 |
| rabbitmq | consumer retry count | 3 |
| websocket | ping interval | 30s |
| websocket | pong timeout | 10s |
