# GoCell Auth 域架构级彻底方案

> 生成日期: 2026-04-19
> 基准: develop@a322d01（PR#188 合并后）；PR#187 待合入
> 关联: `docs/plans/202604181700-domain-driven-plan.md` 域 1

---

## Context

当前 `docs/plans/202604181700-domain-driven-plan.md` 域 1 的 17 条任务（P1-16/17/18 + S18/S19/20/21/22/31/32/35/39/40/41/42/43 + X10）看起来是独立 backlog 条目，但对标 Ory Hydra / Dex / Kratos / Uber fx / Kubernetes apiserver / apimachinery 之后可以看到它们**共享 6 个架构级根因**：

1. JWT 配置无单一事实源（env→main→Issuer.field→slice 副本，verifier 独立再读）
2. Refresh token 语义非 opaque，claim 仅秒级时间戳 + sid，server-side store 与 token 强绑定
3. `runtime/auth.AuthMiddleware` 依赖 cmd/ 层硬编码业务路径匹配器
4. `/internal/v1/` 控制面无一等公民隔离（共用 listener / 仅 prefix 守卫）
5. pkg/errcode 无 infra vs domain 分类 API，service 层各写一套
6. 资源生命周期跨 bootstrap edge 靠 `atomic.Pointer` + lazy worker（credential cleaner 为代表）

**逐条修复的问题**：修完之后架构气味仍在，下一个条目（例如再加一个 middleware、再加一种 token）会复现同样症状。

**本计划目的**：用 6 个"基石件"一次性解决根因；后续把现有 backlog 条目重新归位（多数变成应用基石的薄层修复）。

---

## 架构级问题 → 基石方案映射

| # | 问题 | 当前 backlog 条目 | 基石方案（对标） |
|---|------|-------------------|------------------|
| 1 | JWT 配置漂移 | S18 + S31 + S20 | **F1 — `runtime/auth/config.Registry`**（对标 Hydra `reg.Config().IssuerURL(ctx)`、Kratos `WithParserOptions`）|
| 2 | Refresh token 语义弱 | P1-17 + X10 | **F2 — Refresh token opaque + `RefreshTokenStore`**（对标 Dex `ObsoleteToken` + `reuseInterval` + CAS；Hydra Fosite `GracePeriod`）|
| 3 | Middleware 硬编码业务路径 | S35（PR#187 进行中仅做一半）| **F3 — `runtime/auth.Selector` Builder**（对标 Kratos `middleware/selector`）|
| 4 | 控制面非一等公民 | S32 + R4（域 6）| **F4 — 独立 listener + `RouteGroup`**（对标 kube-apiserver 分端口 + filter chain；etcd client/peer 分离）|
| 5 | Error classification 不一致 | P1-18 + S40 | **F5 — `pkg/errcode.IsInfraError / IsDomainNotFound`**（对标 k8s apimachinery `IsNotFound` 双通道）|
| 6 | Credential sweep 跨 edge 生命周期 | P1-16 + S36（域 9）| **F6 — `runtime/bootstrap.Lifecycle` + 启动期 sweep**（对标 fx `OnStart/OnStop`；GitLab omnibus 短窗 sweep；kubelet bootstrap token TTL）|
| 7 | authn 切换后 principal 未归一化（/internal/v1 delegated auth 断链）| 审查 P1-A（新）| **F7 — `runtime/auth.Principal` 统一契约**（对标 Kratos claims→ctx、chi-jwtauth context 注入、go-grpc-middleware new context） |

---

## 基石件详细设计

### F1. JWT 配置 Registry（单一事实源）

**对标**：
- Hydra `internal/driver/config.DefaultProvider`：所有 JWT 配置归 `urls.self.issuer` 命名空间，env var overlay
- Kratos JWT middleware：`WithParserOptions(jwt.WithIssuers(...), jwt.WithAudiences(...))` 一次注入

**设计**：
```go
// runtime/auth/config/registry.go（新）
type Registry interface {
    Issuer() string                      // 非空强制（real 模式）
    Audiences() []string                 // 支持多 audience allowlist
    SigningKey(ctx context.Context) (*rsa.PrivateKey, error)
    VerificationKeys(ctx context.Context) (map[string]*rsa.PublicKey, error)  // 支持 rotation
    Clock() Clock
}

type Config struct {
    Issuer       string        // GOCELL_JWT_ISSUER
    Audiences    []string      // GOCELL_JWT_AUDIENCES（逗号分隔）
    SigningKeyID string
    KeySources   []KeySource   // 支持多 issuer 迁移
    RealMode     bool          // 非空强制
}

func NewRegistry(cfg Config) (Registry, error) { /* ... */ }
```

**修改路径**：
- 新建 `runtime/auth/config/` 包
- `runtime/auth/jwt.go` 的 `JWTIssuer` / `Verifier` 重构为"消费 Registry"，删除字段级 issuer/audience
- `cmd/core-bundle/main.go::buildJWTDeps` 重构为构造 Registry 一次，Issuer + Verifier + Middleware 共享
- slice（sessionlogin / sessionrefresh）删除 `issuer.DefaultAudience()` 调用，统一从 Registry 读

**吸收 backlog**：S18 (3h) + S31 (1h) + S20 (0.5h)；工时 4.5h → 基石 5h（含测试）

---

### F2. Refresh Token Opaque + Server-side Store（基石重构）

**对标**：
- Dex `storage/storage.go RefreshToken`：`Token` + `ObsoleteToken` 双字段 + `LastUsed`；token 值 `crypto/rand` + base32
- Dex `server/refreshhandlers.go`：`AllowedToReuse` reuseInterval + `UpdateRefreshToken(callback)` CAS
- Hydra：refresh token 全程 opaque（非 JWT）+ 存储端管理生命周期

**设计**：
```go
// runtime/auth/refresh/store.go（新）
type Token struct {
    ID            string    // 对外 token 字符串（opaque，base64url(rand32)）
    ObsoleteToken string    // 上一代 token，reuseInterval 内仍可验
    SessionID     string
    SubjectID     string
    CreatedAt     time.Time
    LastUsed      time.Time
    ExpiresAt     time.Time
}

type Store interface {
    // Issue 原子写入新 token，返回 opaque 字符串
    Issue(ctx context.Context, sessionID, subjectID string, ttl time.Duration) (*Token, error)
    // Rotate 在 CAS 下执行 old → new；reuse 检测返回 ErrTokenReused
    Rotate(ctx context.Context, oldToken string, now time.Time) (*Token, error)
    // Revoke 级联撤销整条 chain
    Revoke(ctx context.Context, sessionID string) error
}

type Policy struct {
    ReuseInterval time.Duration  // 默认 2s（Hydra GracePeriod 默认值）
    MaxAge        time.Duration
}
```

**存储实现**（直接 PG 版，一次到位）：
- 接口：`runtime/auth/refresh/store.go` 定义 `Store` interface + `Token` struct + `Policy`
- 实现：`adapters/postgres/refresh_store.go` — PG 版，`UPDATE refresh_tokens SET token=?, obsolete_token=?, last_used=? WHERE token IN (?, ?) AND revoked_at IS NULL RETURNING *` 实现 CAS
- Migration：`adapters/postgres/migrations/007_refresh_tokens.sql`（独立 table，不强 FK 到 sessions；索引 `(token, revoked_at)`、`(session_id)`、`(expires_at)`）
- 测试：单测用 `runtime/auth/refresh/fake/` 内联 in-memory test double（非独立 impl）；集测走 testcontainers PG
- 连接池复用 PR#169 建立的 `adapters/postgres.Pool`（配置统一入口）

**修改路径**：
- 新建 `runtime/auth/refresh/` 包（store.go / policy.go / fake/）
- 新建 `adapters/postgres/refresh_store.go` + `migrations/007_refresh_tokens.sql`
- `runtime/auth/jwt.go::Issue` 的 refresh token 分支剥离：不再走 JWT，改走 `refresh.Store.Issue`
- `cells/access-core/slices/sessionrefresh/service.go` 重写为：
  - 入参 token 不再 JWT 解析，直接调 `Store.Rotate`
  - reuse detection 由 Store 内 CAS 保证（删除当前基于 `GetByPreviousRefreshToken` 的应用层逻辑）
- `cells/access-core/slices/sessionlogin/service.go`：issue refresh token 改调 `refresh.Store.Issue`
- `cmd/core-bundle/main.go`：wiring PG pool → refresh.Store → sessionlogin/refresh service
- 对外 token 格式变化 → contract schema 更新（仍为 string，长度从 JWT ~300B → opaque 43B）

**吸收 backlog**：P1-17 (3h) + X10 (1-2d) 彻底合并完工；工时 1.5d 一次到位（不拖 Wave 2）

#### F2 接口契约（已锁定 2026-04-19，对标 Dex + Hydra Fosite）

> 实现 PR（接口 + DDL / PG impl / service 切换）必须按本节契约落地，偏离需同步修订。

**C1. Rotate 语义 = CAS + reuse detection + 级联撤销全事务内聚**
- 签名：`Rotate(ctx, presentedToken string, now time.Time) (*Token, error)`
- reuse detection 触发后 Store 内部完成 `Revoke(sessionID)`，调用方仅消费 `ErrTokenReused`
- 对标：Dex `server/refreshhandlers.go::UpdateRefreshToken(callback)` 单事务 CAS

**C2. Token 值生成归 Store**
- 算法：`crypto/rand` 32 bytes → `base64.RawURLEncoding.EncodeToString`（43 字符）
- service 层仅传 `sessionID / subjectID / ttl`；测试通过 `Clock` + `io.Reader` 注入
- 对标：Dex `storage.NewID()`

**C3. Error sentinel — reuse 独立 auth category**
```go
// runtime/auth/refresh/errors.go
var (
    ErrTokenNotFound = errcode.NewDomain("ERR_REFRESH_TOKEN_NOT_FOUND", ...)
    ErrTokenExpired  = errcode.NewDomain("ERR_REFRESH_TOKEN_EXPIRED", ...)
    ErrTokenRevoked  = errcode.NewDomain("ERR_REFRESH_TOKEN_REVOKED", ...)
    ErrTokenReused   = errcode.NewAuth("ERR_REFRESH_TOKEN_REUSED", ...)  // 重放 / 凭证泄露告警信号
)
```
- reuse 不是 domain not-found，是 OAuth2 RFC 6749 §10.4 定义的攻击信号；独立 category 支撑监控告警分级

**C4. Policy + Clock 构造时固化**
- `NewPGStore(pool *pgxpool.Pool, policy Policy, clock Clock) Store`
- Policy（`ReuseInterval` / `MaxAge`）部署期决策，禁止 per-call 传
- `Clock` 独立接口仅为测试替换

**C5. Rotate CAS SQL 两条分支（不用 `IN (token, obsolete)`）**
```sql
-- 分支 A: happy-path rotate
UPDATE refresh_tokens
SET token = $2, obsolete_token = token, last_used = $3
WHERE token = $1 AND revoked_at IS NULL AND expires_at > $3
RETURNING *;

-- 分支 A 返回 0 行 → 分支 B: reuse detection probe
SELECT id, session_id, token, last_used FROM refresh_tokens
WHERE obsolete_token = $1 AND revoked_at IS NULL;
```
Grace period 决策树（对标 Hydra Fosite `GracePeriod` / Dex `AllowedToReuse`）：
- 分支 B 命中 & `now - last_used ≤ ReuseInterval` → 幂等重试，返回当前 current token（不再 rotate）
- 分支 B 命中 & 超 `ReuseInterval` → 事务内 `Revoke(sessionID)` + 返回 `ErrTokenReused`
- 分支 B 未命中 → `ErrTokenNotFound`

**C6. Migration 007 DDL（partial unique + timestamp soft-delete）**
```sql
CREATE TABLE refresh_tokens (
    id             BIGSERIAL PRIMARY KEY,
    token          TEXT        NOT NULL,
    obsolete_token TEXT        NULL,
    session_id     TEXT        NOT NULL,
    subject_id     TEXT        NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at     TIMESTAMPTZ NOT NULL,
    revoked_at     TIMESTAMPTZ NULL
);

CREATE UNIQUE INDEX refresh_tokens_token_active_uq
    ON refresh_tokens (token) WHERE revoked_at IS NULL;

CREATE UNIQUE INDEX refresh_tokens_obsolete_active_uq
    ON refresh_tokens (obsolete_token)
    WHERE obsolete_token IS NOT NULL AND revoked_at IS NULL;

CREATE INDEX refresh_tokens_session_idx ON refresh_tokens (session_id);

CREATE INDEX refresh_tokens_expires_idx
    ON refresh_tokens (expires_at) WHERE revoked_at IS NULL;
```
- soft-delete 用 `timestamptz` 而非 `boolean`：审计可追溯，支撑延迟清理策略
- 活跃 token 唯一仅作用于 `revoked_at IS NULL`，允许历史 revoked 行共享字符串（审计友好）

**C7. Revoke 唯一入口 = by sessionID**
- 签名：`Revoke(ctx, sessionID string) error`
- 不提供 `RevokeByToken` / `RevokeBySubject`
- 登出 / reuse detection / 过期清理统一走 session scope；subject 级撤销由 sessions 层发事件 → session 撤销 → 传导到 refresh store（保持单一入口）

---

### F3. Auth Selector Builder（运行时零业务路径知晓）

**对标**：
- Kratos `middleware/selector/selector.go`：`Builder.Path().Match().Build()` 链式 API，skip 规则 app 层声明
- grpc-ecosystem `go-grpc-middleware/auth`：`AuthFuncOverride` 允许 handler 级豁免

**现状**：PR#187 引入了 `runtime/auth.Policy/Guard`，但只到 handler 层 guard 调用；**`AuthMiddleware` 的 exempt 规则仍由 cmd/main.go:815-825 硬编码注入**。S35 只做了一半（move 注入点，未移除硬编码）。

**设计**（在 PR#187 Policy/Guard 基础上延伸）：
```go
// runtime/auth/selector.go（新）
type MatchFunc func(method, path string) bool

type Selector struct {
    Middleware func(http.Handler) http.Handler
    skipMatch  MatchFunc
}

func NewSelector(mw func(http.Handler) http.Handler) *Selector { /* ... */ }
func (s *Selector) Skip(fn MatchFunc) *Selector { /* 链式追加 */ }
func (s *Selector) Path(pattern string) *Selector { /* Path-based skip helper */ }
func (s *Selector) Build() func(http.Handler) http.Handler { /* ... */ }

// cells/access-core/cell.go 注册时：
// runtime/auth 零硬编码路径；cell 声明自身豁免规则
authSelector := auth.NewSelector(middleware).
    Path("/api/v1/access/users/change-password").
    Path("/api/v1/access/sessions/{id}").  // DELETE self-logout during reset
    Build()
```

**修改路径**：
- 新建 `runtime/auth/selector.go`
- `runtime/auth/middleware.go` 删除 `WithPasswordResetExemptMatcher` / `WithPasswordResetChangeEndpointHint` option，exempt 逻辑全部上提到 Selector 层
- `cmd/core-bundle/main.go:815-825` 硬编码路径全部删除，移到 `cells/access-core/cell.go::RegisterRoutes` 内
- `cells/access-core/cell.go` 新增 `AuthExemptMatcher()` 方法暴露给 bootstrap

**吸收 backlog**：S35 彻底版（PR#187 半做 → 这里完工）+ 搭车 S39 (15min)；工时 2h

---

### F4. 控制面独立 Listener + RouteGroup

**对标**：
- kube-apiserver：`--secure-port` vs `--insecure-port`（已废）vs 独立 loopback；不同 port + 独立 authenticator chain + 默认拒绝
- etcd：client port 2379 / peer port 2380，分别配 TLS
- Hashicorp Vault：`listener.tcp` 可声明多块配置
- Kratos：`middleware/selector` + transport/http `RouteGroup`

**设计**：
```go
// runtime/http/router/group.go（新）
type RouteGroup struct {
    Prefix     string
    Listener   ListenerRef  // primary | internal | health
    Middleware []func(http.Handler) http.Handler
}

// runtime/bootstrap/bootstrap.go 新增
type Option func(*Config)
func WithListener(name string, addr string) Option { ... }
func WithRouteGroup(group RouteGroup) Option { ... }

// 使用
bootstrap.New(
    bootstrap.WithListener("primary", ":8080"),
    bootstrap.WithListener("internal", ":18080"),
    bootstrap.WithListener("health", ":9090"),
    bootstrap.WithRouteGroup(router.RouteGroup{
        Prefix:     "/api/v1",
        Listener:   "primary",
        Middleware: []{jwtAuth, rateLimit},
    }),
    bootstrap.WithRouteGroup(router.RouteGroup{
        Prefix:     "/internal/v1",
        Listener:   "internal",
        Middleware: []{serviceTokenAuth, mTLSRequired},
    }),
    bootstrap.WithRouteGroup(router.RouteGroup{
        Prefix:     "/healthz|/readyz|/metrics",
        Listener:   "health",
        Middleware: []{},  // 无认证
    }),
)
```

**关键特性**：
- 默认拒绝：未匹配任何 RouteGroup 的请求 → 404（对标 kube-apiserver filter chain）
- 编译期保证 listener 引用存在（validate phase 检查）
- internal listener 可选 mTLS（`WithMutualTLS` on listener）

**修改路径**：
- 新建 `runtime/http/router/group.go`
- `runtime/bootstrap/bootstrap.go` 核心重构（+listener pool +route group registry）
- 删除 `WithInternalEndpointGuard`（PR#185 中间态），改用 RouteGroup declarative
- 所有 Cell 的 `RegisterRoutes` 签名调整：接收 `RouteGroupRegistry` 而非 flat router
- `cmd/core-bundle/main.go` 改为显式声明 3 个 RouteGroup

**Blast radius**：500+ 行改动，touches runtime 核心 + 全部 Cell 路由注册 API

**吸收 backlog**：R4 (4-8h) + S32 (1h) + 隐式解决 PR#185 外部审查方案 C；工时 1-1.5d

---

### F5. Errcode Classifier

**对标**：
- k8s apimachinery `IsNotFound / IsForbidden / IsInternalError / IsServerTimeout`：双通道（Reason + HTTP code），infra 绝不映射为 404
- pkg/errors Dave Cheney 风格 Is/As 链

**设计**：
```go
// pkg/errcode/classify.go（新）
func IsInfraError(err error) bool {
    // 1. 显式 category: ErrCategoryInfra
    // 2. errors.Is(ctx.Canceled | ctx.DeadlineExceeded | driver.ErrBadConn | ...)
    // 3. 未注册 code 默认归为 infra（fail-closed）
}

func IsDomainNotFound(err error) bool {
    // 只对白名单 code（ErrXxxNotFound 家族）返回 true
}

func IsExpected4xx(err error) bool {
    // 401/403/404/409/422 等预期客户端错误，用于日志降级
}

// Error 结构增加 Category 字段
type Error struct {
    Code     string
    Category Category  // domain | infra | validation | auth
    // ...
}
```

**修改路径**：
- 扩展 `pkg/errcode/` 现有文件（不新建包）
- 所有现有 `errcode.New(code, message)` 保持兼容；新增 `errcode.NewInfra(code, ...)` 显式 category
- `cells/access-core/slices/sessionvalidate/service.go::logSessionLookupError` 改用 `IsDomainNotFound` 白名单
- `cells/access-core/slices/sessionrefresh/service.go::lookupSession` 改用 `IsInfraError` 分支
- `runtime/auth/middleware.go` 日志降级改用 `IsExpected4xx`

**吸收 backlog**：P1-18 (2h) + S40 (1h) + S43 (1h)；工时 3h（含基石 API 2h + 应用 1h）

---

### F7. 统一 Principal 契约（authn → authz 归一化）

**对标**：
- Kratos `middleware/auth/jwt/jwt.go`：验证成功后 `NewContext(ctx, claims)`，handler 从 ctx 读
- go-chi/jwtauth `jwtauth.go`：`Verifier` 把 token + 错误写 ctx，`Authenticator` 消费 ctx，handler 不回读 transport header
- go-grpc-middleware `interceptors/auth/auth.go`：auth interceptor 返回增强后的 `newCtx`，handler 只读 ctx
- K8s apimachinery `authentication/user.Info`：`Name / UID / Groups / Extra` 四字段统一主体建模

**核心问题（审查 P1-A）**：
- 当前 `/internal/v1` delegated auth：router 标记 delegated → AuthMiddleware skip JWT → ServiceTokenMiddleware 只做 HMAC pass-through → handler 的 `RequireAnyRole` 仍读 JWT claims → **401/403**
- 认证层做了切换但 principal 契约没统一，导致 internal RBAC 写接口"可达但不可用"

**设计**：
```go
// runtime/auth/principal.go（新）
type PrincipalKind int
const (
    PrincipalUser PrincipalKind = iota  // JWT 用户
    PrincipalService                    // service token / mTLS machine
    PrincipalAnonymous                  // 公共端点
)

type Principal struct {
    Kind       PrincipalKind
    Subject    string            // user.id | service name | ""
    Roles      []string          // user roles | service granted roles（如 role:internal-admin）
    AuthMethod string            // "jwt" | "service_token" | "mtls"
    Claims     map[string]string // 已校验字段，按需扩展
}

// context 注入 / 读取
type principalKey struct{}
func WithPrincipal(ctx context.Context, p *Principal) context.Context
func FromContext(ctx context.Context) (*Principal, bool)
func MustFromContext(ctx context.Context) *Principal  // 未注入 panic（defense-in-depth）

// Authenticator 接口（JWT / ServiceToken / mTLS 均实现）
type Authenticator interface {
    Authenticate(ctx context.Context, r *http.Request) (*Principal, error)
}
```

**Service Token → Principal 映射策略**：
- ServiceTokenMiddleware 成功后注入 `Principal{Kind: Service, Subject: "gocell-internal", Roles: ["role:internal-admin"], AuthMethod: "service_token"}`
- `role:internal-admin` 为保留 role，`cells/access-core/.../authz.go` 的 `RequireAnyRole` 匹配时允许穿过
- 后续可扩展：不同 service token 映射不同 roles（多租户 machine principal）

**修改路径**：
- 新建 `runtime/auth/principal.go` + `authenticator.go`
- `runtime/auth/middleware.go::AuthMiddleware` 验证成功后注入 user Principal（替换当前直接注入 claims）
- `runtime/auth/servicetoken.go` 验证成功后注入 service Principal
- `runtime/auth.Policy/Guard`（PR#187）改为消费 Principal；`RequireAnyRole` 改为 `FromContext + role match`
- 全部 handler 的 authz 调用统一：`p := auth.MustFromContext(ctx); if !p.HasRole(...) { ... }`
- 删除 handler 直接读 `ctx.Value(claimsKey)` 的所有残留

**吸收审查条目**：审查 P1-A 彻底版；解锁 `/internal/v1/access/roles/assign|revoke` 真实可用；工时 4h

---

### F6. Bootstrap Lifecycle + 启动期 Sweep

**对标**：
- Uber fx `lifecycle.go`：`OnStart` 串行 + 失败立即逆序 `OnStop`；`OnStop` LIFO
- GitLab omnibus：initial root password 文件启动期无条件 sweep
- kubelet：bootstrap token TTL + 启动期 cleanup loop

**设计**：
```go
// runtime/bootstrap/lifecycle.go（新）
type Hook struct {
    Name    string
    OnStart func(context.Context) error
    OnStop  func(context.Context) error
}

type Lifecycle interface {
    Append(Hook)
}

// bootstrap.New 将 Lifecycle 实例注入给所有 cell/worker
// OnStart 在 RouteGroup 绑定 listener 之前执行
// OnStop 逆序 + 各 hook 独立超时

// runtime/worker/lazy.go（新，搭车域 9 S36）
func Lazy(get func() Worker) Worker { ... }  // atomic.Pointer 封装
```

**应用到 credential sweep**：
```go
// cells/access-core/internal/initialadmin/sweep.go（新）
func (b *Bootstrapper) RegisterLifecycle(lc bootstrap.Lifecycle) {
    lc.Append(bootstrap.Hook{
        Name: "initialadmin-sweep",
        OnStart: func(ctx context.Context) error {
            // 无条件扫 $GOCELL_CREDENTIALS_DIR，mtime 超 TTL 即删
            // 不依赖 adminExists 判断
            return b.sweepAll(ctx)
        },
        OnStop: func(ctx context.Context) error {
            return b.sweepAll(ctx)  // 优雅停止时再清
        },
    })
}
```

**修改路径**：
- 新建 `runtime/bootstrap/lifecycle.go`
- 新建 `runtime/worker/lazy.go`（搭车域 9 S36，框架级 Lazy）
- `cells/access-core/internal/initialadmin/` 重构：删 `lazyBootstrapWorker` 间接层，sweep 直接挂 OnStart
- `runtime/bootstrap/bootstrap.go::Run` 插入 Lifecycle OnStart/OnStop 阶段

**吸收 backlog**：P1-16 (2h) + 域 9 S36 (2h)；工时 4h（含框架 2h + 应用 2h）

---

## 新 PR 拆分（替代现有 Wave A/B）

原 `docs/plans/202604181700-domain-driven-plan.md` 域 1 的 Wave A/B/C/D 被重组为：

### Wave 0 — 基石（新增，主线优先，~5-6 工作日）

| PR | 基石件 | 吸收 backlog | 工时 |
|----|--------|-------------|------|
| PR-AUTH-F5-ERRCODE | F5 errcode classifier | P1-18 + S40 + S43 | 3h |
| PR-AUTH-F6-LIFECYCLE | F6 bootstrap Lifecycle + worker.Lazy + initialadmin 启动期 sweep | P1-16 + S36 + 审查 P1-B | 4h |
| PR-AUTH-F1-JWT-REGISTRY | F1 JWT 配置 Registry | S18 + S31 + S20 | 5h |
| PR-AUTH-F7-PRINCIPAL | F7 Principal 统一契约 + Authenticator 接口 + handler authz 切换 | 审查 P1-A | 4h |
| PR-AUTH-F3-SELECTOR | F3 Selector Builder + 移除 cmd/main.go 硬编码业务路径 | S35 彻底版 + S39 | 2h |
| PR-AUTH-F2-REFRESH-PG | F2 Refresh token opaque + PG store + migration 007 | P1-17 + X10 一次到位 | 1.5d |
| PR-AUTH-F4-ROUTEGROUP | F4 独立 listener + RouteGroup + merged-state e2e | R4 + S32 + PR#185 方案 C + 审查 P2-C | 1.5d |

**Wave 0 顺序**（已锁定）：F5 → F6 → F1 → **F7** → F3 → F2 → F4

顺序理由：
- F5/F6/F1 低耦合基础件先行
- **F7 必须在 F3/F4 之前**：F3 Selector 消费 Principal；F4 RouteGroup 的 middleware chain 也要消费 Principal
- F2 语义重构独立
- F4 blast radius 最大放最后

### Wave 1 — 应用 & 测试（Wave 0 后，~10h）

| PR | 任务 | 来源 |
|----|------|------|
| PR-AUTH-JWT-TEST | S19 + S21 + S22 | 依赖 F1 落地 |
| PR-AUTH-ROLELIST-CURSOR | S42 | 独立 contract（不依赖基石） |
| PR-AUTH-MARSHAL-ERR | S41 | 独立打磨 |
| PR-AUTH-FIRSTRUN-DX | login response 补 userId + 403 hint resolved path + README macOS base64 可移植化 + first-run-setup 验证接口路径修正 | 审查 P2-A + P2-B（1-2h）|

### Wave 2 — 无（F2 Wave 0 已完工）

~~原计划 Wave 2 F2 PG 持久化~~ → 已并入 Wave 0 PR-AUTH-F2-REFRESH-PG 一次完工

---

## 与现有计划的冲突 & 升级点

| 现有 backlog | 原计划 | 新计划 |
|-------------|--------|--------|
| X10 AUTH-REFRESH-OPAQUE | 🟠 PG-REPO 后触发（Wave D，1-2d）| **提前到 Wave 0 F2**（内存版）；PG 持久化放 Wave 2 |
| R4 INTERNAL-LISTENER（域 6）| 🟡 4-8h 规模风险拖延 | **提升主线 F4**（1-1.5d）|
| S32 CONTROLPLANE-TOKEN-PROD-GATE | 🟠 real 模式前触发 | 被 F4 吸收（RouteGroup 默认拒绝 = 生产门禁）|
| PR#185 WithInternalEndpointGuard | 中间态 | F4 落地后**删除该 option** |
| S35 PR#187 半做 | 进行中 | F3 完工（合 PR#187 后追补）|
| S36 WORKER-LAZY-HOIST（域 9）| 🟡 P3 | 升到 F6 基石（框架级 Lazy）|

---

## 关键决策（已确认 2026-04-19）

| # | 决策 | 选择 |
|---|------|------|
| D1 | X10 refresh opaque 时机 | ✅ **Wave 0 一次完工**：直接 PG 版（1.5d），含 migration 007 + pgstore |
| D2 | F4 RouteGroup 现在做 | ✅ **现在做**（1.5d），一次性解决控制面一等公民、R4、S32、PR#185 中间态、审查 P2-C |
| D3 | Wave 0 入场顺序 | ✅ **F5 先行**（3h，最小 blast radius，立基础语义） |
| D4 | 审查 P1-A Principal 契约 | ✅ **新增 F7 基石**（4h，F4 前置） |
| D5 | 审查 P2-A/B 首登 DX | ✅ **Wave 1 搭车**（PR-AUTH-FIRSTRUN-DX，1-2h） |
| D6 | F2 持久化策略 | ✅ **直接 PG 版**：Store 接口 + pgstore + migration 007；内存版仅作单测 fake；Wave 2 取消 |
| D7 | 人力排期 | ✅ **双人并行 Track A + Track B**：~3.5 工作日（vs 单人 ~6 工作日） |

**双人并行排期**（已锁定 D6 + D7）：

```
Track A（认证主链）                  Track B（基础设施 + refresh）
──────────────────────              ──────────────────────────────
D1  F5 Errcode (3h)                 F6 Lifecycle + sweep (4h)
    → F7 Principal 结构体 (1h)
D2  F1 JWT Registry (4h) ──┐        F2 Store 接口 + DDL 007 (3h)
                           │
D3  F7 authenticator (3h)  └──► F2 PG impl(1d) 起步
    → F3 Selector (2h)
D4  F4 RouteGroup 重构 (1d)         F2 集测 + sessionlogin/refresh 切换 (3h)
                                    ──── Track B 汇合 ────
D5  F4 merged-state e2e (0.5d)
```

**Wave 0 总工时（双人并行）**：**~3.5 工作日**
**Wave 0 总工时（单人串行）**：~6 工作日

**并行要点**：
- Track A 改 `runtime/auth/*` + handler authz 切换
- Track B 改 `runtime/bootstrap/*` + `adapters/postgres/*` + `runtime/auth/refresh/*`
- 两 Track 仅在 D2/D3 交界时共享 `runtime/auth/refresh/store.go` 接口（Track B 先出接口，Track A 不触碰）
- 文件冲突极小，合并点仅在 `cmd/core-bundle/main.go`（wiring 层）

---

## 关键文件清单（将被修改或新建）

### 新建
- `runtime/auth/config/registry.go` / `config.go`（F1）
- `runtime/auth/refresh/store.go` / `policy.go` / `fake/` 单测 double（F2）
- `adapters/postgres/refresh_store.go` + `migrations/007_refresh_tokens.sql`（F2 PG impl）
- `runtime/auth/selector.go`（F3）
- `runtime/auth/principal.go` / `authenticator.go`（F7）
- `runtime/http/router/group.go`（F4）
- `runtime/bootstrap/lifecycle.go`（F6）
- `runtime/worker/lazy.go`（F6）
- `pkg/errcode/classify.go`（F5）
- `cells/access-core/internal/initialadmin/sweep.go`（F6 应用）

### 重构
- `cmd/core-bundle/main.go::buildJWTDeps` / `buildAuthMiddleware`（F1/F3/F4/F7 接线）
- `runtime/auth/jwt.go::JWTIssuer` / `Verifier`（F1 消费 Registry；F7 注入 user Principal）
- `runtime/auth/middleware.go`（F3 删 exempt matcher option；F5 日志降级；F7 authn 成功注入 Principal）
- `runtime/auth/servicetoken.go`（F7 注入 service Principal：`role:internal-admin`）
- `runtime/auth.Policy/Guard`（PR#187）（F7 改为消费 Principal 而非直读 claims）
- `runtime/bootstrap/bootstrap.go::Run`（F4 RouteGroup；F6 Lifecycle）
- `cells/access-core/slices/sessionlogin/service.go`（F2 issue 走 Store；Wave 1 补 userId）
- `cells/access-core/slices/sessionrefresh/service.go`（F2 rotate 走 Store；F5 infra 分支）
- `cells/access-core/slices/sessionvalidate/service.go`（F5 errcode 白名单）
- `cells/access-core/**/handler.go`（F7 改用 `auth.FromContext`，删 claims 直读）
- `cells/access-core/cell.go`（F3 AuthExemptMatcher；F4 RouteGroup 注册）
- `cells/access-core/internal/initialadmin/bootstrap.go`（F6 Lifecycle hook）
- `cells/access-core/**/changepassword_e2e_test.go`（F4 验证：删测试 ctx 注入捷径，走 BuildApp）
- `pkg/errcode/*.go`（F5 Category + classifier）

### 复用（已有，无需新建）
- `runtime/auth.Policy` / `Guard`（PR#187）：F3 Selector 在其上延伸
- `runtime/auth.WithInternalEndpointGuard`（PR#185）：F4 落地后删除
- `cells/access-core/internal/initialadmin.Cleaner`（cleaner.go）：F6 OnStop 复用
- `runtime/worker.Worker` 接口：F6 Lazy 包装

---

## 验证方案

### F1 JWT Registry
- **单测**：`runtime/auth/config/registry_test.go`——env var overlay、real 模式非空断言、多 audience allowlist
- **集测**：`cells/access-core/auth_integration_test.go`——issuer 单一来源，drift 检测（编译期 + 运行期双保险）

### F2 Refresh Opaque
- **单测**：`runtime/auth/refresh/memstore/store_test.go`——并发 100 goroutine Rotate，CAS 无重复 token 产出；reuseInterval 内复用；超窗触发 reuse detection 级联撤销
- **集测**：POST `/api/v1/access/sessions/refresh` 真实路由 + 并发重放测试

### F3 Selector
- **单测**：`runtime/auth/selector_test.go`——MatchFunc 链式组合、path pattern、default deny
- **集测**：grep 确认 `cmd/core-bundle/main.go` 无 `/api/v1/` 业务路径字面量

### F4 RouteGroup
- **集测**：三 listener 并行启动，`/internal/v1/` 仅 internal port 可达；未匹配 group 返回 404；mTLS 配置生效
- **冒烟**：real 模式启动无 service-token/mTLS 应 fail-fast（吸收 S32）
- **merged-state e2e（强制）**：新增 `cells/access-core/.../changepassword_e2e_test.go` 走 `cmd/core-bundle.BuildApp(opts...)` 真实装配链路（联动域 8 S29），POST `/internal/v1/access/roles/assign` 用真实 service token header，断言 downstream authz 通过；**删除**当前 e2e 内所有测试 context 直接注入捷径（吸收审查 P2-C）

### F7 Principal 契约
- **单测**：`runtime/auth/principal_test.go`——WithPrincipal/FromContext round-trip、MustFromContext 未注入 panic、HasRole 匹配
- **集测**：`/internal/v1/access/roles/assign` 走真实 service token → Principal 注入 → authz 放行；`/api/v1/` 走 JWT → Principal 注入 → authz 消费；两种 authn 下 handler 代码**完全相同**（均调 `auth.FromContext`）
- **回归**：grep 全仓确认无 handler 直接读 `ctx.Value(claimsKey)`

### F5 Errcode Classifier
- **单测**：`pkg/errcode/classify_test.go` table-driven——category 分类、IsInfraError fail-closed 默认、ctx.Canceled 归 infra
- **集测**：注入 DB 故障，sessionvalidate 日志为 Error，HTTP 500

### F6 Lifecycle + Sweep
- **单测**：Lifecycle OnStart 串行、失败逆序 OnStop、超时独立；Lazy 并发读写
- **集测**：预置过期 credentials 文件重启，OnStart 后文件消失；adminExists 仍触发 sweep

### 总集成
- 全仓 `go test ./... -tags integration`
- `go build ./cmd/... -tags integration`（遵从 memory：`feedback_integration_tag_build.md`）
- `golangci-lint run ./...`（遵从 memory：`feedback_lint_before_push.md`）

---

## 工时总计

| Wave | 双人并行 | 单人串行 | 说明 |
|------|---------|---------|------|
| Wave 0（基石）| ~3.5 工作日 | ~6 工作日 | F5(3h) + F6(4h) + F1(5h) + F7(4h) + F3(2h) + F2 PG(1.5d) + F4(1.5d) |
| Wave 1（应用 & 测试 & DX）| ~1 工作日 | ~1.5 工作日 | JWT-TEST 5h + ROLELIST-CURSOR 1h + MARSHAL-ERR 1h + FIRSTRUN-DX 2h（4 PR 可并行评审）|
| Wave 2 | — | — | 无（F2 PG 已并入 Wave 0） |

**与原计划对比**：原 Wave A+B+C+D ≈ 4-5 工作日（点修，架构债全部保留）；本方案双人 ~4.5 工作日（7 基石完工 + 吸收审查 P1-A/P1-B/P2-A/B/C）。
**收益**：消除 7 个架构气味；解锁 `/internal/v1` RBAC 写接口；统一 authn 契约；refresh token 生产就绪（opaque + PG CAS）；merged-state e2e 走真实装配；control plane 独立 listener。



  Wave 0 — 基石（7 PR，~3.5d 双人 / ~6d 单人）

  #: 1
  PR: PR-AUTH-F5-ERRCODE
  工时: 3h
  前置依赖: 无
  文件范围: pkg/errcode/classify.go（新）+ sessionvalidate/service.go + sessionrefresh/service.go +
    runtime/auth/middleware.go（日志降级）
  验证门禁: IsInfraError table-driven 单测；DB 故障注入集测
  ────────────────────────────────────────
  #: 2
  PR: PR-AUTH-F6-LIFECYCLE
  工时: 4h
  前置依赖: 无
  文件范围: runtime/bootstrap/lifecycle.go（新）+ runtime/worker/lazy.go（新）+ initialadmin/sweep.go（新）+
    initialadmin/bootstrap.go
  验证门禁: 预置过期文件重启 → OnStart 后消失；OnStart 失败触发逆序 OnStop
  ────────────────────────────────────────
  #: 3
  PR: PR-AUTH-F1-JWT-REGISTRY
  工时: 5h
  前置依赖: 无
  文件范围: runtime/auth/config/（新）+ runtime/auth/jwt.go + cmd/core-bundle/main.go::buildJWTDeps
  验证门禁: env var overlay 单测；real 模式 issuer/audience 非空断言
  ────────────────────────────────────────
  #: 4
  PR: PR-AUTH-F7-PRINCIPAL
  工时: 4h
  前置依赖: F1
  文件范围: runtime/auth/principal.go（新）+ authenticator.go（新）+ middleware.go + servicetoken.go + 全部 handler
    authz 切换
  验证门禁: grep 无 handler 直读 ctx.Value(claimsKey)；/internal/v1/access/roles/assign 用 service token → 200
  ────────────────────────────────────────
  #: 5
  PR: PR-AUTH-F3-SELECTOR
  工时: 2h
  前置依赖: F7 + PR#187 合入
  文件范围: runtime/auth/selector.go（新）+ runtime/auth/middleware.go 删 exempt option +
    cmd/core-bundle/main.go:815-825 删硬编码 + cells/access-core/cell.go
  验证门禁: grep 确认 cmd/main.go 无 /api/v1/ 字面量
  ────────────────────────────────────────
  #: 6
  PR: PR-AUTH-F2-REFRESH-PG
  工时: 1.5d
  前置依赖: F1 + F5
  文件范围: runtime/auth/refresh/（新）+ adapters/postgres/refresh_store.go（新）+
    migrations/007_refresh_tokens.sql（新）+ sessionlogin/service.go + sessionrefresh/service.go + contract schema
  验证门禁: testcontainers 100 goroutine 并发 Rotate CAS；reuseInterval 窗口测试
  ────────────────────────────────────────
  #: 7
  PR: PR-AUTH-F4-ROUTEGROUP
  工时: 1.5d
  前置依赖: F3 + F6 + F7
  文件范围: runtime/http/router/group.go（新）+ runtime/bootstrap/bootstrap.go 核心重构 + 全部 Cell RegisterRoutes +
    merged-state e2e
  验证门禁: 三 listener 并行启动；real 模式无 service-token fail-fast；changepassword e2e 走 BuildApp

  Wave 1 — 应用 & DX（4 PR，~1d，4 PR 并行评审）
- 8 PR-AUTH-JWT-TEST 5h  F1 S19 + S21 + S22
- 9 PR-AUTH-ROLELIST-CURSOR 1h S42
- 10 PR-AUTH-MARSHAL-ERR  S41
- 11 PR-AUTH-FIRSTRUN-DX  审查 P2-A + P2-B

  ---
  现在做哪个 — 推荐 D1 起点

  双人并行：
  - Track A 开发者 → PR-AUTH-F5-ERRCODE（3h，起手风险最小）
  - Track B 开发者 → PR-AUTH-F6-LIFECYCLE（4h，独立包）

  单人串行（你一人）：
  - 现在就起 PR-AUTH-F5-ERRCODE。3h 可收，收完同日接 F6（4h）

  为什么 F5 起手：
  1. blast radius 最小（pkg/errcode + 3 个 service.go 文件）
  2. 为 F6/F7/F1/F2 全部基石提供 error category 基础语义
  3. 一次吸收 P1-18 + S40 + S43 三条 backlog
  4. 无外部依赖（不等 PR#187）
