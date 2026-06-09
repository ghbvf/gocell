---
paths:
  - "runtime/**/*.go"
  - "cmd/**/*.go"
  - "examples/**/*.go"
  - "corecells/**/*.go"
---

# Runtime API

> **Auth package path (post PR #615, G-10)**: 所有 `auth.AuthPlan` / `auth.ListenerAuth` /
> `auth.AuthJWT` / `auth.AuthNone{}` / `auth.AuthMTLS{}` / `auth.AuthServiceToken` 及
> `auth.NewAuthJWT` / `auth.NewAuthJWTFromAssembly` / `auth.NewAuthServiceToken` 都来自
> `github.com/ghbvf/gocell/kernel/auth`（不是 `kernel/cell`）。当同一个文件还
> import `runtime/auth` 时，把 kernel/auth 别名为 `kauth`：
> ```go
> import (
>     kauth "github.com/ghbvf/gocell/kernel/auth"
>     "github.com/ghbvf/gocell/runtime/auth"  // runtime side (Mount, ServiceTokenMiddleware, ...)
> )
> ```
> `cell.Registrar` 仍在 `kernel/cell`，是 `Cell.Init(ctx, reg)` 的参数类型（PR #615
> 之前叫 `cell.Registry`）。`cell.PrimaryListener` / `cell.InternalListener` /
> `cell.HealthListener` / `cell.AdminListener` 也留在 `kernel/cell`。`auth.AuthOperator` /
> `auth.NewAuthOperator`（#1505 operator 控制面）同样来自 `kernel/auth`。下面所有示例按此约定。

## Auth 路由声明 + listener 拓扑 + RouteGroup (PR-A14b / PR262 / admin #1505)

每个 Cell 在 `Init(ctx, reg)` 中通过 `reg.RouteGroup(...)` 声明路由组。
每个路由组指定目标 listener、URL 前缀、以及注册回调（`Register func(mux cell.RouteMux) error` — PR-MODE-6: error-first 链路，phase5 把 Register 的错误连同 cell+listener+prefix 上下文 wrap 后冒泡到 `Bootstrap.Run`）。
Bootstrap 在 phase5 drain 所有 `RegistrySnapshot.RouteGroups` 并挂载到对应 listener 的 stdlib `*http.ServeMux` 上。

每条业务路由通过 `auth.Mount(mux, auth.Route{...})` 注册（**不是** `auth.Declare`/`auth.RouteDecl` —— 这两个旧符号已删除）。`auth.Route.Contract` 是 `contractspec.ContractSpec`，承载 method+path+contract id；Mount 自动 strip listener prefix、注册 ServeMux handler（`METHOD /path` 形式）、转发 AuthRouteMeta 给 FinalizeAuth。Mount 返回 `error`（PR-MODE-6 ERROR-FIRST-API）；`auth.MustMount` 已删除（B2-K-02）；**slice handler 内部使用 `auth.Mount` + 错误传播**，让错误一路冒泡到 phase5。

```go
// Slice handler — RegisterRoutes 返回 error，使用 auth.Mount + 错误传播
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) error {
    if err := auth.Mount(mux, auth.Route{
        Contract: specSessionsLogin, // contractspec.ContractSpec — Method+Path+Kind=http
        Handler:  http.HandlerFunc(h.loginHandler.HandleLogin),
        Public:   true,              // JWT 豁免
    }); err != nil {
        return err
    }
    if err := auth.Mount(mux, auth.Route{
        Contract:            specSessionsLogout,
        Handler:             http.HandlerFunc(h.logoutHandler.HandleLogout),
        PasswordResetExempt: true,   // 允许 reset-required token 穿过
    }); err != nil {
        return err
    }
    return nil
}

// Cell.Init — PR-A14b 声明式路由组（PR-MODE-6 错误链路贯通），通过 reg.RouteGroup 注册
func (c *AccessCore) Init(ctx context.Context, reg cell.Registrar) error {
    if err := c.BaseCell.Init(ctx, reg); err != nil {
        return err
    }
    reg.RouteGroup(cell.RouteGroup{
        Listener: cell.PrimaryListener,
        Prefix:   "/api/v1/access",
        Register: func(mux cell.RouteMux) error {
            // mux.Route 的 callback 仍是 func(RouteMux) 无 error 返回，
            // 用 outer-variable closure 捕获 slice 错误并通过 Register 返回
            // 给 phase5。Router.MountRouteGroup 用同样模式。
            var firstErr error
            captureErr := func(err error) {
                if err != nil && firstErr == nil {
                    firstErr = err
                }
            }
            mux.Route("/sessions", func(s cell.RouteMux) {
                captureErr(c.loginHandler.RegisterRoutes(s))
                captureErr(c.logoutHandler.RegisterRoutes(s))
            })
            return firstErr
        },
    })
    reg.RouteGroup(cell.RouteGroup{
        Listener: cell.InternalListener,
        Prefix:   "/internal/v1/access",
        Register: func(mux cell.RouteMux) error {
            return c.rbacAssignHandler.RegisterRoutes(mux)
        },
    })
    return nil
}

// composition root — WithListener(ref, addr, authChain []auth.ListenerAuth, ...ListenerOption)
// JWT auth lives on the listener auth chain; there is no separate WithAuthMiddleware /
// WithAuthDiscovery option (PR262: typed AuthPlan replaces cell.Policy).
//
// AuthPlan 构造函数（PR-MODE-6）是 error-first：`auth.NewAuthJWT(v) (AuthJWT, error)`。
// B2-K-02 已删除 Must* 变体；composition root 改 error-first + 上浮 error。
jwtAuth, err := auth.NewAuthJWTFromAssembly(asm)
if err != nil {
    return nil, fmt.Errorf("NewAuthJWTFromAssembly: %w", err)
}
svcTokenAuth, err := auth.NewAuthServiceToken(nonceStore, ring)
if err != nil {
    return nil, fmt.Errorf("NewAuthServiceToken: %w", err)
}
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithListener(cell.PrimaryListener, ":8080",
        []auth.ListenerAuth{jwtAuth}),
    bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9090",
        []auth.ListenerAuth{svcTokenAuth}),
    bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9091",
        []auth.ListenerAuth{auth.AuthNone{}}),
)
```

**SEC-FAIL-CLOSED**：`authChain` 必须非 nil（PR-MODE-1）。HealthListener 的「无认证」由 loopback 隔离 + 显式 `auth.AuthNone{}` 共同表达；隐式 nil 在 phase0 fail-fast，错误码 `ERR_LISTENER_AUTH_CHAIN_MISSING`。

**PR-MODE-1 新增 sentinel 对照表**：

| 错误码 | 触发路径 | 说明 |
|--------|---------|------|
| `ErrListenerAuthChainMissing` | `bootstrap` phase0 | WithListener 第三参传 nil，启动时 fail-fast |
| `ErrReadyzVerboseUnconfigured` | `runtime/http/health` verboseDecision | /readyz?verbose 请求但未配置 token 且未明确 disable |
| `ErrWebsocketOriginsMissing` | `adapters/websocket` UpgradeHandler | AllowedOrigins 为空，构造时 panic |

### ListenerRef 常量

| 常量 | 物理端口 | 挂载路由 | 默认地址 |
|------|---------|---------|---------|
| `cell.PrimaryListener` | public / API | `/api/v1/*` | `:8080` |
| `cell.InternalListener` | control-plane（cell→cell） | `/internal/v1/*` | `127.0.0.1:9090` |
| `cell.HealthListener` | infra | `/healthz` `/readyz` `/metrics` | `127.0.0.1:9091` |
| `cell.AdminListener`（可选，#1505） | control-plane（operator→system） | `/admin/v1/*` | `127.0.0.1:9093`（loopback） |

> `cell.AdminListener` 是 operator 控制面专用 listener（#1505）：承载 `/admin/v1/*` operator→system 端点（如 framework projection rebuild `POST /admin/v1/projection/{cell}/{name}/rebuild`），区别于 internal listener 的 cell→cell 模型。**可选**——仅当装配了 admin 端点（如 `bootstrap.WithProjectionRebuildEndpoint()`）时声明。认证用 `auth.NewAuthOperator`（operator 凭据 Basic Auth + per-IP 限速），**无 caller-cell allowlist**。双向 affinity（phase0 强制）：`/admin/v1/*` 路径必须挂在 AdminListener 且 AdminListener 只挂 `/admin/v1/*`；AdminListener 必须携带 `AuthOperator`，`AuthOperator` 只能用于 AdminListener。详见 `docs/ops/listener-topology.md` §"Admin Listener"。

### bootstrap.WithListener 认证链 (PR262)

`WithListener(ref, addr, authChain []auth.ListenerAuth, opts...)` 的第三参数是一个
**sealed interface slice**。每个元素实现 `auth.ListenerAuth`（marker method `listenerAuthOK()`）。
**`authChain` 必须显式声明（SEC-FAIL-CLOSED，PR-MODE-1）**：传 nil 在 phase0 fail-fast；显式无认证使用
`[]auth.ListenerAuth{auth.AuthNone{}}`。

| 构造函数 | 说明 | 典型 listener |
|---------|------|--------------|
| `auth.NewAuthJWT(v) (AuthJWT, error)` | JWT 验证（直接注入 IntentTokenVerifier）。error-first；`Must*` 变体已删除（B2-K-02，ADR `202605171800`）。 | PrimaryListener |
| `auth.NewAuthJWTFromAssembly(asm) (..., error)` | JWT 验证（phase4 自动从 authProvider Cell 发现 verifier） | PrimaryListener |
| `auth.NewAuthServiceToken(...) (..., error)` | HMAC-SHA256 service token | InternalListener |
| `auth.AuthMTLS{}` | mTLS — 链验证由 `tls.Config.ClientAuth=RequireAndVerifyClientCert` 在握手层完成（必须配置 WithListenerTLS）；中间件 `runtime/http/middleware.MTLS()` 守 peer cert presence + 把 curated `PeerIdentity{Subject,DNSNames,URIs}` 写入 ctx（`pkg/ctxkeys.PeerIdentityFrom`）；server-side `*tls.Config` 用 `runtime/http/tlsutil.NewServerMTLSConfig(certPEM, keyPEM, clientCAs)` 构造 | InternalListener（高安全场景） |
| `auth.AuthNone{}` | 显式无验证（HealthListener loopback 隔离场景；nil 已被 phase0 拒绝） | HealthListener（loopback 隔离） |
| `auth.NewAuthOperator(username, password []byte, limiter, onAuthFail) (AuthOperator, error)` | operator 控制面 Basic Auth（#1505）——env 凭据 + per-IP 限速 + 恒时比较（HMAC-SHA256 fixed-length digest，无长度/内容 oracle）。username 拒控制字符、password ≥ 8 bytes、limiter 必填非 nil（缺一 error-first 拒）。无 caller-cell allowlist。 | AdminListener（唯一允许，phase0 affinity 强制） |

**多 plan chain 示例**：

```go
// mTLS + service-token 双层守护（外层 transport 证书验证 + 内层 HMAC token 防重放）
svcTokenAuth, err := auth.NewAuthServiceToken(nonceStore, ring)
if err != nil {
    return nil, fmt.Errorf("NewAuthServiceToken: %w", err)
}
bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9090",
    []auth.ListenerAuth{
        auth.AuthMTLS{},   // 外层：peer cert presence check
        svcTokenAuth,      // 内层：HMAC-SHA256 + replay guard
    },
    bootstrap.WithListenerTLS(tlsCfg), // ClientAuth=RequireAndVerifyClientCert + ClientCAs required
)
```

**chain 顺序语义**：当 chain 中同时包含 AuthJWT 和非 JWT plan 时，AuthJWT 必须在第 0 位（phase0 校验）。
运行时执行顺序是非 JWT guard（mTLS / ServiceToken）先作为外层运行，然后 JWT 作为最内层 auth 检查。
声明顺序与运行时执行顺序相反；这是有意为之 — 外层 transport guard 在 JWT 密码学校验之前运行。

### verbose-token 与 listener auth 正交 (PR269 round-3)

`/readyz?verbose=true` 的详情披露守卫**不是** auth scheme — 它只控制 verbose body 是否渲染，不参与 listener 认证决策。配置方式：

```go
bootstrap.WithHealthRoutes(
    bootstrap.WithReadyzVerboseToken(token),    // X-Readyz-Token 必须匹配
)
// 或显式禁用：
bootstrap.WithHealthRoutes(
    bootstrap.WithReadyzVerboseDisabled(),
)
```

token 直接 plumb 到 `runtime/http/health.Handler.SetVerboseToken`；不匹配的 `?verbose=true` 请求拿到 `401 ErrReadyzVerboseDenied` 标准 envelope（`httputil.WritePublicError` 一致出口，含 wire `requestId` 透传；对应 slog 日志键 `request_id`）。该路径与 `WithListener(cell.HealthListener, ..., chain)` 的 listener auth chain 完全独立。

### 单 listener 单 auth scheme（删除 RouteGroup.Auth, PR269 round-3)

`cell.RouteGroup` 不再承载 auth 字段。所有挂载在同一 listener 上的路由共享同一 listener authChain。如果一组路由需要独立的 auth scheme（例如 webhook 收件路径用 HMAC 而业务 API 用 JWT），开新 `cell.ListenerRef` 并用 `bootstrap.WithListener(...)` 单独配置：

```go
const WebhookListener cell.ListenerRef = "webhook"

jwtAuth, err := auth.NewAuthJWTFromAssembly(asm)
if err != nil {
    return nil, fmt.Errorf("NewAuthJWTFromAssembly: %w", err)
}
svcTokenAuth, err := auth.NewAuthServiceToken(store, ring)
if err != nil {
    return nil, fmt.Errorf("NewAuthServiceToken: %w", err)
}
bootstrap.WithListener(cell.PrimaryListener, ":8080",
    []auth.ListenerAuth{jwtAuth})
bootstrap.WithListener(WebhookListener, ":8090",
    []auth.ListenerAuth{svcTokenAuth})
```

历史 `cell.GroupAuth` 接口、`cell.AuthVerboseToken` 类型、`bootstrap.WithLivezAuth/WithReadyzAuth/WithMetricsAuth` options 均已删除。`AUTH-PLAN-04 (LAYER-09)` archtest 仍禁止 cells 直接构造 AuthPlan（composition root 责任）。

### auth.Route 字段

| 字段 | 说明 | 约束 |
|------|------|------|
| `Contract` | `contractspec.ContractSpec`（Method + Path + Kind="http"） | 必填，drives 注册 pattern + span attrs |
| `Handler` | `http.Handler` | 必填，非 nil |
| `Policy` | `auth.Policy` — 路由级策略。当 `Contract.Clients` 非空时，`auth.Mount` 自动注入 `RequireCallerCell` 守卫，handler 无需显式 Policy；显式 Policy 与自动 caller_cell 守卫复合（外层 caller-cell guard → 内层 Policy） | 可选；`Public=true` 时必须为 nil |
| `Public` | JWT 豁免 | 与 `Policy` / `PasswordResetExempt` 互斥 |
| `PasswordResetExempt` | 允许 password-reset token | 与 `Public` / `Bootstrap` 互斥；handler 内做细粒度校验 |
| `Bootstrap` | HTTP Basic Auth（env 操作员凭据）保护 setup/admin endpoint | 与 `Public` / `PasswordResetExempt` / `Policy` 互斥；FMT-27 三方互斥守护；FMT-28 限定路径 `/api/v1/*/setup/admin`；`NewBootstrapMiddleware` 实现 per-IP token-bucket + `subtle.ConstantTimeCompare` |

### listener 分流（PR-A14b；admin 见 #1505）

Bootstrap 为每个声明的 listener 构建独立的 `*router.Router`（内含独立 `*http.ServeMux`）。三个**核心** listener 恒在，外加**可选**的 operator admin listener：

- **primary**：挂 `/api/v1/*` 业务路由；JWT AuthMiddleware（来自 `[]auth.ListenerAuth` 中的 AuthJWT/AuthJWTFromAssembly）。primary listener 显式 404 所有 `/internal/v1/*` 与 `/admin/v1/*` 请求，实现端口级物理隔离。
- **internal**：仅挂 `/internal/v1/*` 路由；AuthServiceToken / AuthMTLS 策略，无 JWT 中间件。
- **health**：仅挂 `/healthz` `/readyz` `/metrics`；框架自动注册，Cell 不声明此 listener。
- **admin**（可选，#1505）：仅挂 `/admin/v1/*` operator→system 控制面路由；`AuthOperator` 凭据闸门，无 JWT 中间件。仅当装配了 admin 端点（如 `WithProjectionRebuildEndpoint()`）时声明；`cmd/corebundle` 不装配它。

详见 `docs/ops/listener-topology.md`。

### `/internal/v1/*` 服务令牌防重放（PR-A25）

internal listener 的 `ServiceTokenMiddleware` 必须带一个 replay-safe `auth.NonceStore`。`cmd/corebundle.buildServiceNonceStore` 默认构造 `auth.InMemoryNonceStore(ttl = ServiceTokenMaxAge + 30s)`（多 pod real 模式改构造 Redis-backed store），喂进 `composition.SharedDeps.NonceStore`。real 模式启动时 `composition.SharedDeps.validate`（经 `NewSharedDeps`）会拒绝 `NonceStoreKindNoop`（返回 `ERR_CONTROLPLANE_NONCE_STORE_MISSING`）+ 拒绝多 pod 下的 in-memory store；外部 composition 消费者同样 fail-closed（#1410——原 cmd 私有 `internalGuardFromEnv` 已 dissolve 为 `buildInternalHMACRing`，control-plane 校验前移进 composition）。多 pod 部署须注入分布式实现（例如 Redis）；in-memory 仅保证单 pod 防重放。

**双重校验闭环（#1410 review F1）**：`SharedDeps.validate` 校验的是**声明的** `SharedDeps.NonceStore`，但真正守 `/internal/v1/*` 的 store 是调用方 `RuntimeOptionsFunc` 放进 listener auth plan 的那个——二者本可不一致（opaque callback 另造 store 绕过校验）。`composition.Builder.Build` 因此把 trusted `SharedDeps.Topology` 经 `bootstrap.WithControlPlaneTopology` 注入 bootstrap，**bootstrap phase0**（`validateAuthServiceTokenPlan`）对 listener 实际持有的 `AuthServiceToken.Store.Kind()` 按 topology 复核（in-memory 多 pod 拒、未知 kind fail-closed 拒），对齐 fx `ValidateApp`「校验实际构造图，非平行声明」。accept/reject 单源为 `kernel/auth.NonceStoreKind.ReplaySafe(requireDistributed)`——composition 配置期与 bootstrap 使用期共用同一谓词，不会漂移。该 bootstrap 复核是 **Medium**（runtime guard）：真正 type-system Hard（让「internal listener 只能用 `SharedDeps.NonceStore`」编译期不可绕）不可达，与 #851/#893/#1282 同族 Go 可见性天花板（won't-do，跟踪 gh #1552；细节见 ADR `202605311000-1085` §Amendment 2026-06-04）。

### FinalizeAuth 生命周期

`Bootstrap.Run` 在所有 `reg.RouteGroup(...)` drain 自 RegistrySnapshot 并挂载完成后自动调用 primary listener 的 `rtr.FinalizeAuth()`：

1. 收集所有 `auth.Mount` 推送的 `AuthRouteMeta`
2. 去重 `(method, path)` — 重复 fail-fast
3. 校验 `IsInternal()` ↔ `/internal/v1/*` 路径一致性（PR-A14a）：internal 路径必须在 InternalListener 上，非 internal 路径不得在 InternalListener 上
4. 编译 public / password-reset-exempt 匹配器
5. 从首个 `POST + PasswordResetExempt=true` 路由派生 password-reset change-endpoint hint
6. AuthMiddleware 在请求时通过 Router 字段 lazy 读取匹配器

### Auth 三路径优先级

每个进入 listener 的请求经历三层鉴权策略，优先级从高到低：

1. **路由级 Policy**（`auth.Route.Policy`）— 仅对该路由生效，覆盖一切
2. **Public / PasswordResetExempt**（`auth.Route.Public: true` 或 `auth.Route.PasswordResetExempt: true`）— 豁免 JWT 验证
3. **Listener 认证链**（`WithListener(ref, addr, authChain)` 的 `authChain`）— 对所有路由生效

优先级规则：
- 路由级 Policy 存在时，Listener 认证链中间件在路由层之前运行（链中间件先于路由 handler）
- `Public: true` 不能与路由级 `Policy` 同时设置（FinalizeAuth fail-fast）
- `Public: true` 是 JWT 豁免标志，只对安装了 JWT 中间件的 listener 有意义
- JWT 单一路径（PR262 / PR-MODE-6）：`auth.NewAuthJWT(v)` 直接注入（error-first；`Must*` 已删除，ADR `202605171800`）；`auth.NewAuthJWTFromAssembly(asm)` 走两阶段——phase0 `runtime/bootstrap/auth_plan_validate.go::validateAuthJWTFromAssemblyPlans` 校验 assembly 实例同一性（拒结构体字面量 / nil / wrapper / 拷贝），phase4 `auth_plan_apply.go` 遍历 `assembly.CellIDs()` 找到唯一实现 `AuthProvider` 的 Cell 并通过 `AuthJWTFromAssembly.SetResolved(verifier)` 注入。运行期 `router.WithAuthMiddleware` 通过 `plan.ResolvedVerifier()` 读取，搭配 FinalizeAuth 编译的 Public/PasswordResetExempt matcher。**没有** `WithAuthMiddleware` / `WithAuthDiscovery` 等 Bootstrap 顶层 Option。
- `/internal/v1/*` 路由（`IsInternal()` 为 true）必须挂在 InternalListener；非 internal 路径不得挂在 InternalListener（FinalizeAuth 双向 fail-fast 校验）。

### 规则

- GET 自动覆盖 HEAD（RFC 7231 §4.3.2）
- `(method, path)` 重复出现 → FinalizeAuth 返回 error（保护配置清洁度）
- `Path` 经过 `path.Clean` 规范化
- Handler 内业务校验优先级高于 Route Policy（如 logout 校验 session 归属）
- CORS OPTIONS：当前无 CORS middleware；如需公开 OPTIONS 请显式 `auth.Mount` + `Public: true`
- 禁止在 `cmd/*` / `examples/*/main.go` 硬编码业务路径字面量（`grep '"POST /api/v1/"'` 必须为空）
- Cell 禁止直接 import `runtime/http/router`；通过 `cell.RouteMux` / `cell.RouteGroup` 声明路由（LAYER-07）
- Cell 禁止构造 AuthPlan 值（`auth.NewAuthJWT` / `auth.NewAuthServiceToken` 等所有变体；`Must*` 已删除，ADR `202605171800`）；认证计划由 composition root（`cmd/`）组装后通过 `WithListener` 注入（LAYER-09 / AUTH-PLAN-04）

### Internal endpoint caller-cell allowlist (A5)

Service token 采用 4-part 格式 `ts:nonce:callerCell:mac`，`callerCell` 段携带调用方 cell ID。

- `ContractSpec.Clients` 由 codegen 从 `contract.yaml endpoints.clients` 复制（`tools/codegen/contractgen/builder.go`）。L6 governance 规则 **FMT-31** 在 YAML 源头强制 `/internal/v1/*` 路径声明非空 `endpoints.clients`；运行时 `kernel/contractspec.ContractSpec.validateHTTP()` 双向兜底（含 inverse 方向：非 internal 路径必须无 Clients）。
- 当 `Clients` 非空时，`auth.Mount` 自动注入 `RequireCallerCell` 守卫；不在 allowlist 的 caller 返回 403，handler 无需显式 `Policy`。
- 测试用 `auth.TestServiceContext(callerCell)` 注入 service principal。

## Option 范式分层（强依赖 fail-fast vs 累加式 builder noop）

GoCell 的 functional-option API 分两类，nil 处理语义相反——**新加 option 前必须先决定属于哪一类**，混用会让调用方对错误时机产生错觉。判断规则：「这个 option 同名调用是否累加？」 累加 → builder；不累加（一次声明一个依赖）→ wiring。

| 类别 | 语义 | nil 入参处理 | 错误时机 | 现有实例 |
|------|------|------------|---------|---------|
| **强依赖 wiring option** | 一次调用 = 主动声明一个不可替代依赖；同名重复调用 = wiring 矛盾 | option 体内 `validation.IsNilInterface(v) → flag = true; return`，配合 phase0 / `NewForListener` sentinel 校验 | 启动期 fail-fast，错误信息含 option 名 | `bootstrap.WithRateLimiter` / `WithCircuitBreaker` / `WithManagedResource` / `WithManagedCloser` / `router.WithRateLimiter` / `router.WithCircuitBreaker` / `router.WithAuthMiddleware` |
| **累加式 builder option** | 同名 option 可多次调用，最后非空值胜出；nil 入参 = "本次没新数据"，不应清掉之前已设置的值 | option 体内 `if validation.IsNilInterface(v) { return }`（silent noop），最终 nil 校验在 factory `NewXxx` 内 fail-fast | factory 调用期 fail-fast | `outbox.WithTxManager`（最终校验在 `NewService`，`OUTBOX-SERVICE-01` archtest 守）；`config.WithKeys` / `WithKeySeparate` / `WithEnvClock`（最终校验在 `NewJWTIssuerFromRegistry` / `NewJWTVerifierFromRegistry`） |

约束：

- 两类 option **不可同时混用一个名字**。`WithFoo` 要么走 wiring fail-fast，要么走 builder noop。
- 选 wiring 时，sentinel 标志位（如 `b.rateLimiterNil`）+ `phase0ValidateOptions` / `NewForListener` 中校验是统一形式；godoc 必须明文「Both bare-nil and typed-nil ... are rejected at phase0」。
- 选 builder 时，godoc 必须明文「Typed-nil inputs are not stored; the subsequent factory call will fail with ...」+ 在 factory 入口对最终字段做 `validation.IsNilInterface` 校验。
- `pkg/validation.IsNilInterface` 是 **kernel/ + runtime/ 层唯一**的 typed-nil 反射 helper（`ERROR-FIRST-TYPED-NIL-01` archtest 锁定识别 pattern），不得新建并行 helper。kernel/ 允许依赖 pkg/（见 `go-standards.md` 分层规则），无 file-local 同义 helper 残留——`kernel/outbox.isNilEmitterDependency` / `kernel/cell.IsNilHookObserver` 已删除并改用单源 helper。`kernel/clock.MustHaveClock` 不属于该治理范围：它是 panic-style guard（programmer error，被 ConsumerBase / NewDirectEmitter 等 wiring 调用），与 typed-nil bool helper 不同语义层。

ref: uber-go/fx app.go — Option 模式；强依赖 nil 在 dig.Provide 注册期立即 errInvalidInput。
ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go — 强依赖 fail-fast / 弱依赖 substitute。
ref: go-kratos/kratos app.go — 弱依赖 substitute（builder noop 同源范式）。
ref: ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md` — Must* 删除与
error-first 改造（B2-K-02）；composition root 的 `cell.MustNewAuth*` 已删除，统一改用
error-first `auth.NewAuth*`（PR #615 G-10 之后符号位于 `kernel/auth`；与
`runtime/auth` 同导入时使用 `kauth` 别名）。
