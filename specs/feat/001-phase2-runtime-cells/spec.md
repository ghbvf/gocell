# Spec — Phase 2: Runtime + Built-in Cells

> Branch: `feat/001-phase2-runtime-cells`
> 基于: Phase 0+1 kernel 底座（59 Go 文件, 全部编译通过 & 测试绿色）
> 日期: 2026-04-05
> 修订: 2026-04-05 (S3 Decide 采纳 14 项决策)

---

## 1. 概述

Phase 2 将 GoCell 从"可编译的元数据治理框架"升级为"可运行的 Cell-native Go 框架"。交付三大块：

1. **kernel/ 接口扩展** — outbox.Subscriber 接口、Cell 可选注册钩子（HTTPRegistrar / EventRegistrar）
2. **runtime/ 运行时层** — HTTP 中间件链 / 路由 / 健康检查、配置加载与热更新、统一启动器与优雅关闭、可观测性（Prometheus + OTel + slog）、后台 worker、认证鉴权抽象中间件、in-process EventBus
3. **cells/ 内建业务 Cell** — access-core (7 slices)、audit-core (4 slices)、config-core (5 slices) 的 Go 实现，使其在 core-bundle assembly 中可编译、可启动、可验证

Phase 2 的所有 Cell 业务逻辑通过 kernel/ 定义的接口与 adapter 解耦，不依赖具体存储/消息中间件实现（Phase 3 交付）。Phase 2 使用 in-memory 实现（repository port 桩 + 内存 EventBus），Phase 3 替换为真实 adapter。

**Slice 计数说明**: 仓库 YAML 实际为 access-core 7 / audit-core 4 / config-core 5 = 16 slices，与原 roadmap（5/3/4）有差异。本 spec 以实际 YAML 为准（详见 decisions.md 决策 R-1/R-2/R-3）。

---

## 2. 功能需求（FR）

### FR-1: runtime/http — HTTP 基础设施

#### FR-1.1: 中间件链

系统必须提供 7 个 `func(http.Handler) http.Handler` 签名的 chi 兼容中间件：

| 中间件 | 文件 | 职责 |
|--------|------|------|
| RequestID | `runtime/http/middleware/request_id.go` | 从 `X-Request-Id` 读取或生成 UUID，写入 ctx 和响应头 |
| RealIP | `runtime/http/middleware/real_ip.go` | 从 `X-Forwarded-For` / `X-Real-Ip` 提取客户端 IP |
| Recovery | `runtime/http/middleware/recovery.go` | panic 捕获 → 500 响应 + slog.Error |
| AccessLog | `runtime/http/middleware/access_log.go` | 请求/响应结构化日志（method, path, status, duration, request_id） |
| SecurityHeaders | `runtime/http/middleware/security_headers.go` | X-Content-Type-Options, X-Frame-Options, Strict-Transport-Security 等 |
| BodyLimit | `runtime/http/middleware/body_limit.go` | 请求体大小限制（可配置，默认 1MB） |
| RateLimit | `runtime/http/middleware/rate_limit.go` | 基于 token bucket 的限流（per-IP，可配置 rate/burst） |

RateLimit 验收标准：超限请求返回 **429** + `Retry-After` 头；默认 100 req/s, burst 200；不同 IP 的限流桶相互隔离。接受 `RateLimiter` 接口（`Allow(key string) bool`），Phase 2 提供 in-memory 实现，Phase 3 可替换 Redis 实现。**已知限制**: per-IP 内存限流在多实例部署下不共享状态。

**对标参考**: Kratos `middleware/`、go-zero `rest/handler/`

#### FR-1.2: 健康检查

- `/healthz` — liveness probe，聚合 `Assembly.Health()` 结果
- `/readyz` — readiness probe，支持注册自定义 readiness checker
- 返回 200 (healthy) 或 503 (unhealthy) + JSON body `{"status": "healthy|unhealthy", "cells": {...}, "dependencies": {...}}`

#### FR-1.3: 路由构建器

- 基于 `go-chi/chi/v5` 的路由注册器
- 支持 `Route(pattern, handler)` / `Group(prefix, middlewares..., routes)` / `Mount(prefix, handler)`
- 自动注册 `/healthz` + `/readyz` + `/metrics`（如 observability 启用）
- 支持 API 版本前缀 `/api/v1/`

### FR-2: runtime/config — 框架启动配置

**与 FR-10 config-core 的分工边界**: `runtime/config` 负责框架启动配置（server.http.port, log.level 等），纯本地 YAML+env，生命周期 = 进程级，消费者 = runtime 自身和 Cell.Init。`config-core` Cell 负责业务配置管理（feature flag, 租户配置, 发布/回滚），数据持久化在 DB，通过 contract 暴露给其他 Cell。**禁止** config-core 内部 import runtime/config。

#### FR-2.1: 配置加载

- 从 YAML 文件 + 环境变量加载配置
- 环境变量优先级高于 YAML 文件
- 支持 `Config` 接口：`Get(key) any` / `Scan(dest interface{}) error`
- 支持嵌套 key（`server.http.port`）
- Cell.Init 阶段通过 Dependencies.Config 获取 runtime/config 加载的配置

**对标参考**: go-micro `config/Source`、Kratos `config/`

#### FR-2.2: 文件变更 Watcher

- 基于 fsnotify 监控配置文件变更
- 变更时触发回调函数 `OnChange(key string, value any)`
- 支持注册多个 watcher
- watcher 错误不影响主进程运行

### FR-3: runtime/bootstrap — 统一启动器

- `Bootstrap` struct: `New(opts...) → Bootstrap`
- 启动流程: parse config → init assembly → register cells → start HTTP server → start workers → block until signal
- 支持选项模式: `WithConfig(path)` / `WithHTTP(addr)` / `WithWorkers(workers...)` / `WithAssembly(assembly)`
- 启动阶段任一步骤失败 → 有序回滚已启动组件

**对标参考**: Uber fx `app.go`、Kratos `app.go`

### FR-4: runtime/shutdown — 优雅关闭

- 监听 SIGINT / SIGTERM
- 关闭超时可配置（默认 30s）
- 关闭顺序: stop workers → drain HTTP connections → stop assembly（反序 Stop cells）→ close config watcher
- 超时后强制退出

### FR-5: runtime/observability — 可观测性

#### FR-5.1: Prometheus 指标

- 指标注册器 + HTTP 中间件（请求计数、延迟直方图、按 method/path/status 分组）
- `/metrics` 端点暴露
- Cell 可注册自定义指标

#### FR-5.2: OpenTelemetry Tracing

- Tracer provider 初始化 + HTTP 中间件（自动创建 span）
- 支持 trace_id / span_id 注入到 context
- 支持配置 exporter（stdout / OTLP）

#### FR-5.3: slog 结构化日志

- slog Handler 自动从 ctx 提取 trace_id / span_id / request_id / cell_id 等字段
- 支持 JSON 和 text 格式输出
- 日志级别运行时可调

**对标参考**: Kratos `middleware/tracing/`、`middleware/metrics/`

### FR-6: runtime/worker — 后台 Worker

- `Worker` 接口: `Start(ctx) error` / `Stop(ctx) error`
- `WorkerGroup`: 管理多个 worker 并发启动/关闭
- 支持周期性 job: `NewPeriodicWorker(interval, fn)`
- worker panic 隔离，不影响其他 worker

**对标参考**: go-zero `ServiceGroup`

### FR-7: runtime/auth — 认证鉴权抽象框架

**与 FR-8 access-core 的分工边界**: `runtime/auth` 只提供**抽象中间件框架**，定义 `TokenVerifier` 和 `Authorizer` 接口。access-core 的 session-validate 提供 `TokenVerifier` 具体实现（JWT RS256 验证 + session 状态查询），authorization-decide 提供 `Authorizer` 具体实现（RBAC 策略判定）。Bootstrap 阶段注入。runtime/ 不耦合具体认证策略。

#### FR-7.1: 认证中间件

- `AuthMiddleware(verifier TokenVerifier) func(http.Handler) http.Handler`
- `TokenVerifier` 接口: `Verify(ctx, token string) (Claims, error)`
- Claims 注入 context（sub, roles, exp 等）
- verifier 返回 error → 401

#### FR-7.2: 授权中间件

- `RequireRole(authorizer Authorizer, roles ...string)` 中间件
- `Authorizer` 接口: `Authorize(ctx, subject, resource, action string) (bool, error)`
- 授权失败 → 403

#### FR-7.3: 服务间认证

- `ServiceToken` 中间件用于服务间调用
- 基于 shared secret 的 HMAC 签名校验
- **注**: Phase 2 实现但 Journey 验证延迟到 Phase 3

**对标参考**: go-micro `auth/`、Kratos `middleware/auth/`

### FR-8: cells/access-core — 身份与会话管理

基于已有 7 个 slice.yaml 实现 Go 代码：

| Slice | 职责 | 关键操作 |
|-------|------|---------|
| identity-manage | 用户 CRUD + 锁定/解锁 | Create/Get/Update/Delete/Lock/Unlock User → 发布 event.user.created.v1 / event.user.locked.v1 |
| session-login | 密码登录 + JWT 签发（Phase 2 仅密码，OIDC 延迟到 Phase 3 适配器就绪） | Authenticate → Create Session → 签发 access+refresh token → 发布 event.session.created.v1 |
| session-refresh | Token 刷新 | Validate refresh token → 签发新 token pair → 滚动过期 |
| session-logout | Session 吊销 | Revoke session → 发布 event.session.revoked.v1 |
| session-validate | Session/Token 校验 | Validate access token → 返回 Claims |
| authorization-decide | RBAC 权限判定 | Evaluate(subject, resource, action) → Allow/Deny |
| rbac-check | 角色检查 | HasRole / ListRoles（可与 authorization-decide 共用 domain，接口分离） |

Cell 级产出：
- `cell.go` — AccessCore struct 实现 Cell 接口 + HTTPRegistrar + EventRegistrar
- `internal/domain/` — User / Session / Role / Permission 领域模型（Cell 级共享，所有 7 个 Slice 通过构造函数注入共用）
- `internal/ports/` — UserRepository / SessionRepository / RoleRepository 接口（Phase 2 in-memory 桩实现，Phase 3 替换为 adapter）
- session-validate 提供 `runtime/auth.TokenVerifier` 实现
- authorization-decide 提供 `runtime/auth.Authorizer` 实现

**Slice 依赖注入模式**: 每个 Slice 通过 `NewXxxSlice(repo, publisher, logger)` 构造函数注入依赖。`Slice.Init(ctx)` 仅做状态检查。Cell.Init 构造所有 Slice 实例并传入 Cell 级共享资源（repositories、publisher、logger）。

### FR-9: cells/audit-core — 审计链

基于已有 4 个 slice.yaml 实现：

| Slice | 职责 | 关键操作 |
|-------|------|---------|
| audit-append | 审计条目写入 | Append(entry) → HMAC-SHA256 hash chain → 发布 event.audit.appended.v1 |
| audit-verify | 完整性验证 | Verify(range) → 校验 hash chain → 发布 event.audit.integrity-verified.v1 |
| audit-archive | 审计归档（Phase 2 stub 实现，返回 not-implemented） | Archive(before date) → 归档旧条目 |
| audit-query | 审计查询 | Query(filters) → 返回审计记录 |

Cell 级产出：
- `cell.go` — AuditCore struct 实现 Cell 接口 + EventRegistrar
- `internal/domain/` — AuditEntry / HashChain 领域模型
- `internal/ports/` — AuditRepository / ArchiveStore 接口（AuditRepository 方法签名含 ctx 事务抽象，Phase 2 内存实现忽略事务，Phase 3 DB 实现从 ctx 提取事务）
- 消费事件（通过 EventRegistrar 注册订阅）: event.session.created.v1 / event.session.revoked.v1 / event.user.created.v1 / event.user.locked.v1 / event.config.changed.v1 / event.config.rollback.v1
- HMAC 密钥管理: Phase 2 从启动配置注入，不支持运行时轮换。密钥配置为空时启动失败并给出明确错误。
- **已知限制**: Phase 2 hash chain 为内存存储，进程重启后需重建。audit-archive 为 stub 实现。

### FR-10: cells/config-core — 配置管理

基于已有 5 个 slice.yaml 实现：

| Slice | 职责 | 关键操作 |
|-------|------|---------|
| config-write | 配置 CRUD + 版本管理 | Create/Update/Delete config → 发布 event.config.changed.v1 |
| config-read | 配置读取 | Get/List config → serve http.config.get.v1 |
| config-publish | 配置发布 + 回滚 | Publish(version) / Rollback(version) → 发布 event.config.changed.v1 / event.config.rollback.v1 |
| config-subscribe | 配置订阅 | 消费 event.config.changed.v1 → 更新本地缓存 |
| feature-flag | Feature Flag | Get/Evaluate flag → serve http.config.flags.v1。Phase 2 范围: (1) 布尔开关 on/off；(2) 百分比 rollout（按 subject hash 取模）。规则引擎（租户/IP/属性）延迟到 Phase 3+ |

Cell 级产出：
- `cell.go` — ConfigCore struct 实现 Cell 接口 + HTTPRegistrar + EventRegistrar
- `internal/domain/` — ConfigEntry / ConfigVersion / FeatureFlag 领域模型
- `internal/ports/` — ConfigRepository / FlagRepository 接口

**与 FR-2 runtime/config 的分工边界**: config-core 管理业务配置（数据源 = DB），runtime/config 管理框架启动配置（数据源 = 本地 YAML+env）。config-core 不 import runtime/config。

### FR-11: 文档需求

- 系统必须提供 runtime/ 层 API 文档（每个 package 的 doc.go 包含使用示例）
- 系统必须提供 Cell 开发指南文档（如何基于 runtime/ 创建自定义 Cell）
- 系统必须更新 README.md 反映 Phase 2 新增能力

### FR-12: DevOps 需求

- 系统必须确保 `go build ./...` 编译通过（含 cmd/core-bundle 入口）
- 系统必须确保 `gocell validate` 对所有 YAML 元数据校验通过
- 系统必须确保 CI 可运行 `go test ./... -cover` 并输出覆盖率报告
- Makefile 或 taskfile 需包含 build / test / validate / generate 目标

### FR-13: 测试需求

- runtime/ 每个包必须有 `*_test.go`，覆盖率 >= 80%
- cells/ 每个 slice 的 service 层必须有 table-driven 单元测试，覆盖率 >= 80%
- kernel/ 层现有测试必须持续通过，覆盖率维持 >= 90%
- 每个 Cell 的 cell.go 必须有生命周期集成测试（Init → Start → Health → Stop）
- contract test: 每个 contract 的 provider slice 必须有契约测试验证接口签名
- journey test: 8 条 journey 的 auto passCriteria 必须有对应的测试入口

---

## 3. 非功能需求（NFR）

### NFR-1: 依赖隔离

- `kernel/` 不 import `runtime/`、`cells/`、`adapters/`（已有约束，Phase 2 维持）
- `runtime/` 可依赖 `kernel/` 和 `pkg/`（Phase 2 确认合规，需更新 CLAUDE.md）
- `runtime/` 不 import `cells/` 或 `adapters/`
- `cells/` 依赖 `kernel/` + `runtime/`，不 import `adapters/`
- `cells/` 之间不直接 import 另一个 Cell 的 `internal/`
- 由 `gocell validate` 的 TOPO 规则和 go build 共同保证

### NFR-2: 外部依赖控制

Phase 2 新增外部依赖白名单（6 个直接依赖）：
- `github.com/go-chi/chi/v5` — HTTP 路由
- `golang.org/x/crypto` — 密码哈希
- `github.com/fsnotify/fsnotify` — 配置文件 watcher
- `github.com/prometheus/client_golang` — Prometheus 指标
- `go.opentelemetry.io/otel` — OpenTelemetry tracing
- `github.com/golang-jwt/jwt/v5` — JWT RS256 解析验证

不主动引入除白名单以外的直接依赖。不引入 ORM / DI 框架 / 其他 HTTP 框架。传递依赖（transitive）不计入白名单限制。

### NFR-3: 错误处理

- 所有对外错误使用 `pkg/errcode`，禁止裸 `errors.New`
- 统一错误响应格式：`{"error": {"code": "ERR_*", "message": "...", "details": {}}}`
- domain 层不返回 HTTP 状态码

### NFR-4: 日志规范

- 全部使用 `slog` 结构化日志
- 禁止 `fmt.Println` / `log.Printf`
- Error 级别必须含完整 error + 关联业务字段

### NFR-5: 认知复杂度

- 函数认知复杂度 <= 15

---

## 4. 对标参考矩阵

实施前必须按 CLAUDE.md 对标规则拉取参考源码。

| 模块 | Primary Framework | Primary 文件路径 | Secondary |
|------|-------------------|-----------------|-----------|
| runtime/bootstrap | Uber fx | `uber-go/fx/app.go` | Kratos `go-kratos/kratos/app.go` |
| runtime/http/middleware | Kratos | `go-kratos/kratos/middleware/` | go-zero `zeromicro/go-zero/rest/handler/` |
| runtime/http/router | chi | `go-chi/chi/mux.go` | — |
| runtime/config | go-micro | `go-micro/config/` | Kratos `go-kratos/kratos/config/` |
| runtime/worker | go-zero | `zeromicro/go-zero/core/service/servicegroup.go` | Watermill `ThreeDotsLabs/watermill/message/` |
| runtime/auth/jwt | go-micro | `go-micro/auth/` | — |
| runtime/observability | Kratos | `go-kratos/kratos/middleware/tracing/`, `metrics/` | — |
| cells/ lifecycle | Kubernetes | `kubernetes/kubernetes/staging/src/k8s.io/api/core/v1/types.go` | — |
| kernel/outbox event model | Watermill | `ThreeDotsLabs/watermill/message/message.go` | — |

---

## 5. 技术架构

### 5.1 目录结构（新增）

```
├── runtime/
│   ├── http/
│   │   ├── middleware/
│   │   │   ├── request_id.go
│   │   │   ├── real_ip.go
│   │   │   ├── recovery.go
│   │   │   ├── access_log.go
│   │   │   ├── security_headers.go
│   │   │   ├── body_limit.go
│   │   │   └── rate_limit.go
│   │   ├── health/
│   │   │   └── health.go
│   │   └── router/
│   │       └── router.go
│   ├── config/
│   │   ├── config.go
│   │   └── watcher.go
│   ├── bootstrap/
│   │   └── bootstrap.go
│   ├── shutdown/
│   │   └── shutdown.go
│   ├── observability/
│   │   ├── metrics/
│   │   │   └── metrics.go
│   │   ├── tracing/
│   │   │   └── tracing.go
│   │   └── logging/
│   │       └── logging.go
│   ├── worker/
│   │   ├── worker.go
│   │   └── periodic.go
│   ├── auth/
│   │   ├── jwt/
│   │   │   └── jwt.go
│   │   ├── rbac/
│   │   │   └── rbac.go
│   │   └── servicetoken/
│   │       └── servicetoken.go
│   └── eventbus/
│       └── eventbus.go
├── cells/
│   ├── access-core/
│   │   ├── cell.go
│   │   ├── cell_test.go
│   │   ├── internal/
│   │   │   ├── domain/
│   │   │   │   ├── user.go
│   │   │   │   ├── session.go
│   │   │   │   └── role.go
│   │   │   └── ports/
│   │   │       ├── user_repo.go
│   │   │       ├── session_repo.go
│   │   │       └── role_repo.go
│   │   └── slices/
│   │       ├── identity-manage/
│   │       │   ├── handler.go
│   │       │   ├── service.go
│   │       │   └── service_test.go
│   │       ├── session-login/
│   │       │   ├── handler.go
│   │       │   ├── service.go
│   │       │   └── service_test.go
│   │       ├── session-refresh/  (同结构)
│   │       ├── session-logout/   (同结构)
│   │       ├── session-validate/ (同结构)
│   │       ├── authorization-decide/ (同结构)
│   │       └── rbac-check/       (同结构)
│   ├── audit-core/
│   │   ├── cell.go
│   │   ├── cell_test.go
│   │   ├── internal/
│   │   │   ├── domain/
│   │   │   │   ├── entry.go
│   │   │   │   └── hashchain.go
│   │   │   └── ports/
│   │   │       ├── audit_repo.go
│   │   │       └── archive_store.go
│   │   └── slices/
│   │       ├── audit-append/     (handler + service + test)
│   │       ├── audit-verify/     (同结构)
│   │       ├── audit-archive/    (同结构)
│   │       └── audit-query/      (同结构)
│   └── config-core/
│       ├── cell.go
│       ├── cell_test.go
│       ├── internal/
│       │   ├── domain/
│       │   │   ├── config_entry.go
│       │   │   ├── version.go
│       │   │   └── feature_flag.go
│       │   └── ports/
│       │       ├── config_repo.go
│       │       └── flag_repo.go
│       └── slices/
│           ├── config-write/     (handler + service + test)
│           ├── config-read/      (同结构)
│           ├── config-publish/   (同结构)
│           ├── config-subscribe/ (同结构)
│           └── feature-flag/     (同结构)
```

### 5.2 kernel/ 接口扩展（Phase 2 新增）

#### 5.2.1 outbox.Subscriber 接口

在 `kernel/outbox/outbox.go` 新增 `Subscriber` 接口，与已有 `Publisher` 对称：

```go
// Subscriber consumes events from a topic.
type Subscriber interface {
    Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error
    Close() error
}
```

对标: Watermill `message.Subscriber`

#### 5.2.2 Cell 可选注册钩子

在 `kernel/cell/interfaces.go` 新增两个可选接口（不修改现有 Cell 接口）：

```go
// HTTPRegistrar is optionally implemented by Cells that expose HTTP endpoints.
type HTTPRegistrar interface {
    RegisterRoutes(mux RouteMux)
}

// EventRegistrar is optionally implemented by Cells that subscribe to events.
type EventRegistrar interface {
    RegisterSubscriptions(sub outbox.Subscriber)
}

// RouteMux is a minimal route registration interface (kernel/ 不 import chi).
type RouteMux interface {
    Handle(pattern string, handler http.Handler)
    Group(fn func(RouteMux))
}
```

Bootstrap 对每个 Cell 做类型断言调用。Cell 聚合其所有 Slice 的路由后统一注册，Slice 不直接触碰路由器。

### 5.3 依赖图

```
stdlib + go-chi + x/crypto + fsnotify + prometheus + otel + golang-jwt
    │
    ▼
kernel/outbox (新增 Subscriber)
kernel/cell   (新增 HTTPRegistrar / EventRegistrar / RouteMux)
    │
    ▼
runtime/http ────────────────┐
runtime/config ──────────────┤
runtime/bootstrap ───────────┤── 依赖 kernel/ + pkg/，不依赖 cells/ adapters/
runtime/shutdown ────────────┤
runtime/observability ───────┤
runtime/worker ──────────────┤
runtime/auth ────────────────┤
runtime/eventbus ────────────┘
    │
    ▼
cells/access-core ───┐
cells/audit-core  ───┼── 依赖 kernel/ + runtime/，不依赖 adapters/
cells/config-core ───┘
    │
    ▼
cmd/core-bundle ──── 组装入口（硬编码注册顺序: config-core → access-core → audit-core）
```

### 5.4 Cell 间通信

Cell 之间通过 contract 通信，本 Phase 实现方式为 in-process event bus（`runtime/eventbus/`），Phase 3 替换为 RabbitMQ adapter。

- **HTTP contract**: 同进程内通过接口直接调用（Cell 实现 HTTPRegistrar 注册路由，Bootstrap 挂载到 chi Router）
- **Event contract**: `runtime/eventbus/` 内存实现，同时实现 `outbox.Publisher` 和 `outbox.Subscriber`

**事件总线语义（Phase 2 in-memory）**:
- **at-most-once**: 进程重启丢失未消费事件（诚实标注，不模拟持久化）
- consumer 返回 error 时重试 3 次（指数退避），超限路由 dead letter channel（内存，可观测但不持久化）
- topic-based pub/sub，不支持消费组 / 偏移管理
- **Phase 3 切换**: Cell 代码通过 `outbox.Publisher` / `outbox.Subscriber` 接口解耦，Phase 3 替换为 RabbitMQ 实现无需修改 Cell 代码

### 5.5 Slice 依赖注入模式

所有 16 个 Slice 统一采用**构造时注入**：

```go
// 示例: session-login slice
func NewSessionLoginSlice(
    sessionRepo ports.SessionRepository,
    publisher    outbox.Publisher,
    logger       *slog.Logger,
) *SessionLoginSlice { ... }
```

- `Slice.Init(ctx)` 签名不变，仅做状态检查
- Cell.Init 构造所有 Slice 实例并传入 Cell 级共享资源
- 4 个 session-* Slice 共享 Cell 级 `internal/domain/` 和 `internal/ports/`（allowedFiles 扩展为 `cells/access-core/**`）

### 5.6 Bootstrap 与 Assembly 职责划分

- **Assembly** (kernel/): 只管 Cell 生命周期（Register / Start / Stop / Health）
- **Bootstrap** (runtime/): 顶层编排器，负责 Assembly + HTTP + Worker + Config + EventBus 的完整生命周期
- Bootstrap 启动流程: parse config → init eventbus → init assembly (注入 config + publisher) → Cell.Init → Cell.Start → register HTTP routes (HTTPRegistrar) → register event subscriptions (EventRegistrar) → start HTTP server → start workers → block until signal
- 错误处理: Bootstrap 负责外层回滚，Assembly.Start 失败时 LIFO 回滚已启动 Cell

---

## 6. 并行化策略

```
Wave 0 (前置，阻塞后续所有 Wave):
  YAML 元数据修正 — 修正 slice.yaml contractUsages 遗漏 + contract.yaml subscribers 更新
  kernel/ 接口扩展 — outbox.Subscriber + HTTPRegistrar / EventRegistrar
  CLAUDE.md 更新 — 补充 "runtime/ 可依赖 kernel/ 和 pkg/"
  gocell validate 验证零 error

Wave 1 (独立，可全部并行):
  runtime/http/middleware (7 文件)
  runtime/config
  runtime/shutdown
  runtime/worker
  runtime/auth (抽象接口: TokenVerifier + Authorizer + ServiceToken)
  runtime/observability (metrics + tracing + logging)
  runtime/eventbus (in-memory Publisher + Subscriber)

Wave 2 (依赖 Wave 1):
  runtime/http/health  ← 依赖 assembly.Health()
  runtime/http/router  ← 依赖 middleware + health
  runtime/bootstrap    ← 依赖 config + router + worker + shutdown + eventbus
  cells/ domain + ports (可与 Wave 2 runtime 并行: 不依赖 HTTP 的部分)

Wave 3 (依赖 Wave 2, 3 个 Cell 可并行):
  cells/access-core ──┐
  cells/audit-core  ──┼── handler + service + test (依赖 router + eventbus)
  cells/config-core ──┘

Wave 4 (集成):
  cmd/core-bundle 入口更新 (硬编码注册顺序: config-core → access-core → audit-core)
  Journey 端到端验证: 5 Hard Gate + 3 Soft Gate
```

**Wave 2→3 最小可启动条件**: config 加载 + router 注册 + eventbus 就绪 + Cell Init/Start 生命周期可运行。observability/auth 中间件可在 Wave 3 后期集成。

---

## 7. Gate 验证

### Hard Gate（必须 PASS）

```bash
# 编译
cd src && go build ./...

# 单元测试 + 覆盖率
go test ./runtime/... -cover   # >= 80%
go test ./cells/... -cover     # >= 80%
go test ./kernel/... -cover    # >= 90% (维持)

# 元数据校验
./cmd/gocell/gocell validate   # 零 error

# Cell 验证
./cmd/gocell/gocell verify cell --id=access-core
./cmd/gocell/gocell verify cell --id=audit-core
./cmd/gocell/gocell verify cell --id=config-core

# Journey Hard Gate (单 Cell / 密码登录路径)
./cmd/gocell/gocell verify journey --id=J-sso-login          # 密码登录路径，OIDC criteria 为 manual
./cmd/gocell/gocell verify journey --id=J-session-refresh
./cmd/gocell/gocell verify journey --id=J-session-logout
./cmd/gocell/gocell verify journey --id=J-user-onboarding
./cmd/gocell/gocell verify journey --id=J-account-lockout
```

### Soft Gate（跨 Cell 事件驱动，允许 stub/mock 辅助）

```bash
./cmd/gocell/gocell verify journey --id=J-audit-login-trail   # 跨 Cell，依赖 in-memory EventBus
./cmd/gocell/gocell verify journey --id=J-config-hot-reload   # 跨 Cell，依赖 EventBus
./cmd/gocell/gocell verify journey --id=J-config-rollback     # 跨 Cell，依赖 EventBus
```

Soft Gate 在 Phase 2 通过 in-memory EventBus 验证基本流程，Phase 3 adapter 就绪后做完整端到端验证。
