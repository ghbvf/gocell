# GoCell 完整能力清单

> 更新日期: 2026-04-05 | Phase 0-3 完成后

---

## 1. 分层架构总览

| 层 | 模块数 | 状态 | 说明 |
|----|--------|------|------|
| **kernel/** | 11 包 | 全部 IMPL | cell/assembly/metadata/governance/outbox/idempotency/journey/registry/scaffold/slice + schemas |
| **runtime/** | 11 子包 | 全部 IMPL | auth/bootstrap/config/eventbus/http×3/observability×3/shutdown/worker |
| **adapters/** | 6 包 | 全部 IMPL | postgres/redis/rabbitmq/oidc/s3/websocket |
| **cells/** | 3 Cell, 16 slices | 全部 IMPL | access-core(7s) / audit-core(4s) / config-core(5s) |
| **cmd/** | 2 CLI | 全部 IMPL | gocell (validate/scaffold/generate/check/verify) + core-bundle |
| **pkg/** | 5 包 | 全部 IMPL | errcode/ctxkeys/httputil/id/uid |
| **contracts/** | 13 YAML | 声明完成 | 5 HTTP + 8 Event |
| **journeys/** | 8 YAML | 声明完成 | SSO/onboarding/lockout/refresh/logout/audit-trail/hot-reload/rollback |
| **infra** | 4 服务 | 配置完成 | Docker Compose (PG/Redis/RabbitMQ/MinIO) + Makefile |
| **docs** | 28 文件 | 完成 | 架构/指南/评审/参考 |

---

## 2. Kernel 层（11 包）

### 2.1 cell — Cell/Slice/Contract 核心模型
- `Cell` interface — ID/Type/ConsistencyLevel/Init/Start/Stop/Health/Ready
- `Slice` interface — ID/BelongsToCell/ConsistencyLevel/Init/Verify
- `Contract` interface — ID/Kind/OwnerCell/ConsistencyLevel/Lifecycle
- `Assembly` interface — Register/Start/Stop/Health
- `BaseCell` struct — 状态机实现 + sync.Mutex 线程安全 + shutdownCtx
- `BaseSlice` / `BaseContract` — 基础实现
- `CellType` enum — core/edge/support
- `Level` enum — L0(LocalOnly)/L1(LocalTx)/L2(OutboxFact)/L3(WorkflowEventual)/L4(DeviceLatent)
- `ContractKind` enum — http/event/command/projection
- `ContractRole` enum — serve/call/publish/subscribe/handle/invoke/provide/read
- `HTTPRegistrar` / `EventRegistrar` / `RouteMux` — 可选注册接口

### 2.2 assembly — 应用装配
- `CoreAssembly` struct — Register/Start(按注册顺序)/Stop(反序) + 状态守卫
- `Generator` — 代码生成（main.go + boundary.yaml）

### 2.3 metadata — 元数据解析
- `Parser` — 遍历 cells/contracts/journeys/assemblies YAML
- `ProjectMeta` — 聚合所有元数据
- `CellMeta`/`SliceMeta`/`ContractMeta`/`JourneyMeta`/`AssemblyMeta` — 类型定义
- JSON Schema 嵌入（cell/slice/contract/assembly/journey/status-board/actors）

### 2.4 governance — 架构治理
- `Validator` — 元数据验证引擎
- REF 规则 — 引用完整性（belongsToCell→cell 存在等）
- TOPO 规则 — 拓扑合法性（role 匹配 kind、一致性等级约束）
- VERIFY 规则 — 闭环验证（所有角色 verify.contract）
- FMT 规则 — 格式校验（JSON Schema + 枚举 + 禁用字段名 FMT-10）
- ADV 规则 — 警告级（journey coverage gap）
- `DependencyChecker` — 循环依赖检测
- `TargetSelector` — 文件→slice→cell→journey 映射

### 2.5 outbox — Outbox Pattern 接口
- `Writer` interface — `Write(ctx, Entry) error`（context-embedded tx 约定）
- `Relay` interface — `Start(ctx)/Stop(ctx)`
- `Publisher` interface — `Publish(ctx, topic, payload)`
- `Subscriber` interface — `Subscribe(ctx, topic, handler)/Close()`
- `Entry` struct — ID(idempotency 标识)/AggregateID/EventType/Payload/Metadata

### 2.6 idempotency — 幂等检查接口
- `Checker` interface — `IsProcessed(ctx, key)/MarkProcessed(ctx, key, ttl)`

### 2.7 journey — Journey 目录
- `Catalog` — Get/List/CellJourneys/ContractJourneys/CrossCellJourneys

### 2.8 registry — 注册表
- `CellRegistry` — Get/SlicesFor/AllIDs
- `ContractRegistry` — Get/ByKind/ByOwner/Producers/Consumers

### 2.9 scaffold — 脚手架
- `Scaffolder` — CreateCell/Slice/Contract/Journey（text/template 生成 YAML）

### 2.10 slice — Slice 验证
- `Runner` — VerifySlice/VerifyCell/RunJourney（包装 go test -run）

### 2.11 metadata/schemas — JSON Schema
- 7 个 schema（cell/slice/contract/assembly/journey/status-board/actors）
- `//go:embed *.json` 内嵌

---

## 3. Runtime 层（11 子包）

### 3.1 auth — 认证授权
- `JWTVerifier` — RS256 验证 + exp/iss/aud 检查
- `JWTIssuer` — RS256 签发
- `LoadKeysFromEnv()` — 环境变量加载 RSA 密钥对
- `AuthMiddleware` — 公开/保护端点白名单
- `ServiceToken` — HMAC 签名 + timestamp + 5min 窗口
- `Claims` struct — UserID/Roles/Expiry

### 3.2 bootstrap — 应用启动
- `Bootstrap` struct — config→assembly→HTTP→workers 编排
- `WithPublisher(outbox.Publisher)` + `WithSubscriber(outbox.Subscriber)` — 接口注入
- `WithEventBus` — 向后兼容便利方法
- `WithWorkers` — 注册后台 worker

### 3.3 config — 配置管理
- YAML + 环境变量覆盖
- `Watcher` — 文件变更触发 + shutdownCtx 集成

### 3.4 eventbus — 内存事件总线
- `InMemoryEventBus` — topic-based pub/sub + 3x retry + dead letter
- `Health()` — 总线健康状态
- 实现 `outbox.Publisher` + `outbox.Subscriber`

### 3.5 http/router — HTTP 路由
- chi-based `Router` — Handle/Route/Mount/Group
- 实现 `cell.RouteMux` 接口

### 3.6 http/health — 健康检查
- `/healthz` + `/readyz` 端点
- 集成 Assembly.Health()

### 3.7 http/middleware — 7 个中间件
- `RequestID` — UUID 生成 + 长度限制 + 控制字符拒绝
- `RealIP` — trustedProxies 配置，仅信任列表内 proxy 的 XFF
- `Recovery` — panic 恢复 → 500
- `AccessLog` — slog 结构化访问日志
- `SecurityHeaders` — HSTS/CSP/X-Frame-Options
- `BodyLimit` — 请求体大小限制 → 413
- `RateLimit` — 固定窗口限流 + 动态 Retry-After

### 3.8 observability/logging — 结构化日志
- slog handler + trace_id/span_id 关联

### 3.9 observability/metrics — 指标
- `Collector` 接口 — `RecordRequest(method, path, status, duration)`
- `InMemoryCollector` — dev/test 用的计数/直方图/快照实现
- 生产实现: `adapters/prometheus` (PR#42)

### 3.10 observability/tracing — 追踪
- `Tracer` / `Span` 接口 — `Start(ctx, name) (ctx, Span)`
- `simpleTracer` — dev/test 用的随机 ID 实现
- 生产实现: `adapters/otel` (PR#42, OTel SDK + OTLP gRPC exporter)

### 3.11 shutdown — 优雅关闭
- `Manager` — signal→timeout→LIFO hook 执行（失败不中断后续）

### 3.12 worker — 后台任务
- `WorkerGroup` — 并行启动 + 串行反序停止
- `PeriodicWorker` — 定时任务 + panic 隔离

---

## 4. Adapters 层（6 包）

### 4.1 postgres — PostgreSQL (pgx/v5)
- `Pool` — 连接池 + DSN/env 配置 + Health()
- `TxManager` — RunInTx + savepoint 嵌套 + panic 回滚 + context-embedded tx
- `Migrator` — pressly/goose v3 wrapper + embed.FS + up/down/status (PR#42)
- `OutboxWriter` — 实现 outbox.Writer + fail-fast ERR_ADAPTER_NO_TX
- `OutboxRelay` — 实现 outbox.Relay + worker.Worker + FOR UPDATE SKIP LOCKED + batch 100 + 72h cleanup
- `RowScanner` — pgx Row/Rows 抽象（QueryBuilder 已迁至 `pkg/query.Builder`）
- migrations/001_create_outbox_entries.sql

### 4.2 redis — Redis (go-redis/v9)
- `Client` — standalone/sentinel + Health/Close
- `DistLock` — Redlock TTL + Lua atomic release/renew + 续租 goroutine
- `IdempotencyChecker` — 实现 idempotency.Checker (SET NX + TTL)
- `Cache` — Get/Set/Delete + TTL + JSON 泛型 helper

### 4.3 rabbitmq — RabbitMQ (amqp091-go)
- `Connection` — AMQP URL + exponential backoff 重连 + channel 池 + Health
- `Publisher` — 实现 outbox.Publisher + confirm mode
- `Subscriber` — 实现 outbox.Subscriber + SubscriberConfig (QueueName/PrefetchCount)
- `ConsumerBase` — idempotency.Checker + 3x retry + DLQ routing + slog 可观测
- `PermanentError` — 标记不可重试错误直接进 DLQ

### 4.4 oidc — thin go-oidc v3 wrapper
- `Adapter` — 懒初始化 go-oidc Provider + Refresh（metadata/JWKS 轮转）
- `Verifier()` — 返回 go-oidc `IDTokenVerifier`
- `OAuth2Config()` — 返回 `oauth2.Config`（调用方直接用于 token exchange/userinfo）
- 不复制 SDK 类型，暴露 go-oidc/oauth2 原生类型

### 4.5 s3 — thin aws-sdk-go-v2 wrapper
- `Client` — aws-sdk-go-v2 S3 client + Health (HeadBucket)
- `Upload` — 实现 ObjectUploader 接口
- `SDK()` — 暴露底层 `*s3.Client` 用于 download/delete/presigned 等操作
- 不包装 SDK 已有能力（Download/Delete/PresignedURL 通过 SDK() 直接使用）

### 4.6 websocket — WebSocket (nhooyr.io/websocket)
- `Hub` — 连接管理 (register/unregister/broadcast/unicast)
- Signal-first 模式（推送轻量刷新信号）
- `UpgradeHandler` — HTTP 升级 + origin 检查
- ping/pong + 超时断开

---

## 5. Cells 层（3 Cell, 16 Slices）

### 5.1 access-core (L2, core)
| Slice | 功能 | 端点 |
|-------|------|------|
| session-login | 密码登录 + JWT 签发 | POST /api/v1/access/sessions/login |
| session-logout | 会话注销 + 事件发布 | POST /api/v1/access/sessions/logout |
| session-refresh | Token 刷新 + rotation + reuse detection | POST /api/v1/access/sessions/refresh |
| session-validate | 会话验证 | GET /api/v1/access/sessions/validate |
| identity-manage | 用户 CRUD + 锁定/解锁 + PATCH | CRUD /api/v1/users |
| rbac-check | RBAC 权限检查 | POST /api/v1/access/rbac/check |
| authorization-decide | 权限决策 | POST /api/v1/access/authorize |

Domain: User (PasswordHash/Status/CreatedAt) + Session (TokenPair/ExpiresAt/PreviousRefreshToken) + Role
Ports: UserRepository + SessionRepository + RoleRepository
Adapters: internal/mem (in-memory)

### 5.2 audit-core (L3, core)
| Slice | 功能 |
|-------|------|
| audit-append | 事件追加 + HMAC-SHA256 hash chain |
| audit-verify | 完整性验证 |
| audit-query | 审计日志查询 + time.Parse 400 校验 |
| audit-archive | S3 归档（stub） |

Domain: AuditEntry (PrevHash/Hash/Payload)
Ports: AuditRepository + ArchiveStore
Adapters: internal/mem + internal/adapters/postgres (AuditRepository PG) + internal/adapters/s3archive (ArchiveStore wrapper)

### 5.3 config-core (L2, core)
| Slice | 功能 |
|-------|------|
| config-read | 配置读取 |
| config-write | 配置 CRUD + outbox 事件 |
| config-publish | 版本发布 + 回滚 |
| config-subscribe | 变更订阅（event consumer） |
| feature-flag | 特性开关评估 |

Domain: ConfigEntry (Key/Value/Version) + FeatureFlag (Key/Enabled/Rollout)
Ports: ConfigRepository + FlagRepository
Adapters: internal/mem + internal/adapters/postgres (ConfigRepository PG)

---

## 6. CLI 工具

### gocell
| 命令 | 功能 |
|------|------|
| `gocell validate` | 运行全部治理规则（REF/TOPO/VERIFY/FMT/ADV） |
| `gocell scaffold cell` | 创建 Cell 骨架 (cell.yaml + Go 包) |
| `gocell scaffold slice` | 创建 Slice 骨架 |
| `gocell scaffold contract` | 创建 Contract 骨架 |
| `gocell scaffold journey` | 创建 Journey 骨架 |
| `gocell generate assembly` | 生成 main.go + boundary.yaml |
| `gocell check` | 架构分析（依赖/拓扑） |
| `gocell verify slice/cell/journey` | 运行测试 |

### core-bundle
- 3 Cell 运行时入口（access-core + audit-core + config-core）
- adapter 接线（环境变量切换 in-memory / real）

---

## 7. Contracts（13 个）

### HTTP Contracts
| ID | Owner | Level |
|----|-------|-------|
| http.auth.login.v1 | access-core | L1 |
| http.auth.me.v1 | access-core | L1 |
| http.auth.refresh.v1 | access-core | L1 |
| http.config.get.v1 | config-core | L1 |
| http.config.flags.v1 | config-core | L1 |

### Event Contracts
| ID | Owner | Level | 特性 |
|----|-------|-------|------|
| event.session.created.v1 | access-core | L2 | replayable, at-least-once |
| event.session.revoked.v1 | access-core | L2 | replayable, at-least-once |
| event.user.created.v1 | access-core | L2 | replayable |
| event.user.locked.v1 | access-core | L2 | replayable |
| event.audit.appended.v1 | audit-core | L3 | replayable, at-least-once |
| event.audit.integrity-verified.v1 | audit-core | L3 | — |
| event.config.changed.v1 | config-core | L2 | replayable |
| event.config.rollback.v1 | config-core | L2 | replayable |

---

## 8. Journeys（8 个）

| Journey | 涉及 Cell | 类型 |
|---------|----------|------|
| J-sso-login | access-core, audit-core, config-core | 跨 Cell |
| J-user-onboarding | access-core | 单 Cell |
| J-account-lockout | access-core | 单 Cell |
| J-session-refresh | access-core | 单 Cell |
| J-session-logout | access-core | 单 Cell |
| J-audit-login-trail | audit-core, access-core | 跨 Cell |
| J-config-hot-reload | config-core | 单 Cell |
| J-config-rollback | config-core | 单 Cell |

---

## 9. 基础设施

### Docker Compose 服务
| 服务 | 镜像 | 端口 | 用途 |
|------|------|------|------|
| PostgreSQL | postgres:15-alpine | 5432 | 持久化 + outbox |
| Redis | redis:7-alpine | 6379 | 缓存 + 分布式锁 + 幂等 |
| RabbitMQ | rabbitmq:3.12-management | 5672/15672 | 消息队列 + DLQ |
| MinIO | minio/minio:latest | 9000/9001 | S3 对象存储 |

### Makefile
- `make build/test/validate/generate/cover/clean` — Go 构建
- `make up/down` — Docker Compose
- `make test-integration` — 集成测试（docker compose + go test -tags=integration）
- `make healthcheck-verify` — 30s 健康检查

### 外部依赖（直接）
| 依赖 | 用途 |
|------|------|
| go-chi/chi/v5 | HTTP 路由 |
| golang-jwt/jwt/v5 | JWT 签发/验证 |
| jackc/pgx/v5 | PostgreSQL 驱动 |
| redis/go-redis/v9 | Redis 客户端 |
| rabbitmq/amqp091-go | RabbitMQ AMQP |
| nhooyr.io/websocket | WebSocket |
| fsnotify/fsnotify | 文件监听 |
| golang.org/x/crypto | bcrypt |
| stretchr/testify | 测试断言 |
| gopkg.in/yaml.v3 | YAML 解析 |

---

## 10. 统计

| 指标 | 数值 |
|------|------|
| Go 源文件 | ~200+ |
| 测试包 | 60 |
| 代码行数（Phase 3 累计） | ~16K |
| kernel 覆盖率 | 93-100% |
| 全量 go test | 60/60 PASS |
| gocell validate | 0 errors |
| Tech-debt 活跃 | 12 条 |

---

## 11. Phase 4 待实现

| 项目 | 类型 |
|------|------|
| examples/sso-bff | 示例：SSO 完整登录 |
| examples/todo-order | 示例：CRUD + 事件驱动 |
| examples/iot-device | 示例：L4 设备管理 |
| testcontainers 真实集成测试 | tech-debt |
| RS256 完全默认化 | tech-debt |
| CI pipeline | tech-debt |
| Grafana dashboard 模板 | roadmap 延迟 |
| VictoriaMetrics adapter | roadmap 延迟 |
