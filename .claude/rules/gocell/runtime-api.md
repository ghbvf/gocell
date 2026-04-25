---
paths:
  - "runtime/**/*.go"
  - "cmd/**/*.go"
  - "examples/**/*.go"
  - "cells/**/*.go"
---

# Runtime API

## Auth 路由声明 + 三 listener + RouteGroup (PR-A14b)

每个 Cell 实现 `RouteGroupContributor` 接口，通过 `RouteGroups()` 声明路由组。
每个路由组指定目标 listener、URL 前缀、以及注册回调（`Register func(mux cell.RouteMux)`）。
Bootstrap 在 phase5 收集所有路由组并挂载到对应 listener 的 chi.Mux 上。

每条业务路由通过 `auth.Mount(mux, auth.Route{...})` 注册（**不是** `auth.Declare`/`auth.RouteDecl` —— 这两个旧符号已删除）。`auth.Route.Contract` 是 `wrapper.ContractSpec`，承载 method+path+contract id；Mount 自动 strip listener prefix、注册 chi handler、转发 AuthRouteMeta 给 FinalizeAuth。

```go
// Cell.RouteGroups — PR-A14b 声明式路由组
func (c *AccessCore) RouteGroups() []cell.RouteGroup {
    return []cell.RouteGroup{
        {
            Listener: cell.PrimaryListener,
            Prefix:   "/api/v1/access",
            Register: func(mux cell.RouteMux) {
                mux.Route("/sessions", func(s cell.RouteMux) {
                    auth.Mount(s, auth.Route{
                        Contract: specSessionsLogin, // wrapper.ContractSpec — Method+Path+Kind=http
                        Handler:  http.HandlerFunc(c.loginHandler.HandleLogin),
                        Public:   true,                     // JWT 豁免
                    })
                    auth.Mount(s, auth.Route{
                        Contract:            specSessionsLogout,
                        Handler:             http.HandlerFunc(c.logoutHandler.HandleLogout),
                        PasswordResetExempt: true,         // 允许 reset-required token 穿过
                    })
                })
            },
        },
        {
            Listener: cell.InternalListener,
            Prefix:   "/internal/v1/access",
            Register: func(mux cell.RouteMux) {
                auth.Mount(mux, auth.Route{
                    Contract:  specRolesAssign,
                    Handler:   http.HandlerFunc(c.rbacAssignHandler.HandleAssign),
                    Delegated: true,
                })
            },
        },
    }
}

// composition root — WithListener(ref, addr, defaultPolicy, ...ListenerOption)
// JWT auth lives on the listener policy (PolicyJWT / PolicyJWTFromAssembly);
// there is no separate WithAuthMiddleware / WithAuthDiscovery option.
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyJWTFromAssembly(asm)),
    bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9090", bootstrap.PolicyServiceToken(ring)),
    bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9091", cell.Policy{}),
)
```

### ListenerRef 常量

| 常量 | 物理端口 | 挂载路由 | 默认地址 |
|------|---------|---------|---------|
| `cell.PrimaryListener` | public / API | `/api/v1/*` | `:8080` |
| `cell.InternalListener` | control-plane | `/internal/v1/*` | `127.0.0.1:9090` |
| `cell.HealthListener` | infra | `/healthz` `/readyz` `/metrics` | `127.0.0.1:9091` |

### bootstrap.WithListener 策略

每个 listener 在构建时绑定一个默认 Policy，决定所有未经 `auth.Mount` 路由级 Policy 覆盖路由的鉴权行为：

| Policy | 说明 | 典型 listener |
|--------|------|--------------|
| `PolicyJWT(verifier)` | JWT 验证（直接注入 IntentTokenVerifier） | PrimaryListener |
| `PolicyJWTFromAssembly(asm)` | JWT 验证（phase4 自动从 authProvider Cell 发现 verifier） | PrimaryListener |
| `PolicyServiceToken` | HMAC-SHA256 service token | InternalListener |
| `PolicyMTLS` | mTLS — 仅断言存在 peer cert；链验证由 `tls.Config.ClientAuth=RequireAndVerifyClientCert` 在握手层完成（必须配置 WithListenerTLS） | InternalListener（高安全场景） |
| `PolicyVerboseToken` | bearer token gate for ?verbose probes | HealthListener readyz route group（可选） |
| `PolicyNone` | 无验证 | HealthListener（loopback 隔离） |
| `PolicyStack(a, b)` | 组合策略 | 任意（注意：JWT 不可嵌入 stack — Bootstrap 通过 Name=="jwt" 提取 verifier） |

### auth.Route 字段

| 字段 | 说明 | 约束 |
|------|------|------|
| `Contract` | `wrapper.ContractSpec`（Method + Path + Kind="http"） | 必填，drives 注册 pattern + span attrs |
| `Handler` | `http.Handler` | 必填，非 nil |
| `Policy` | `auth.Policy` — 路由级策略 | 可选；`Public=true` 时必须为 nil |
| `Public` | JWT 豁免 | 与 `Policy` / `PasswordResetExempt` 互斥 |
| `PasswordResetExempt` | 允许 password-reset token | 与 `Public` 互斥；handler 内做细粒度校验 |
| `Delegated` | `/internal/v1/*` 路由一致性标记 | `Delegated: true` ⇔ 路径必须以 `/internal/v1/` 开头且路由组挂在 `InternalListener`。FinalizeAuth 校验一致性并 fail-fast (PR-258 F2 round-3：router-aware listener identity 检查)。 |

### 三 listener 分流（PR-A14b）

Bootstrap 为每个声明的 listener 构建独立的 `*router.Router`（内含独立 chi.Mux）：

- **primary**：挂 `/api/v1/*` 业务路由；JWT AuthMiddleware。primary listener 显式 404 所有 `/internal/v1/*` 请求，实现端口级物理隔离。
- **internal**：仅挂 `/internal/v1/*` 路由；ServiceToken / mTLS 策略，无 JWT 中间件。
- **health**：仅挂 `/healthz` `/readyz` `/metrics`；框架自动注册，Cell 不声明此 listener。

详见 `docs/ops/listener-topology.md`。

### `/internal/v1/*` 服务令牌防重放（PR-A25）

internal listener 的 `ServiceTokenMiddleware` 必须带一个 replay-safe `auth.NonceStore`。`cmd/corebundle.internalGuardFromEnv` 默认构造 `auth.InMemoryNonceStore(ttl = ServiceTokenMaxAge + 30s)`。real 模式启动时 `SharedDeps.Validate` 会拒绝 `NonceStoreKindNoop`（返回 `ERR_CONTROLPLANE_NONCE_STORE_MISSING`）。多 pod 部署须通过 `auth.WithServiceTokenNonceStore(sharedStore)` 注入分布式实现（例如 Redis）；in-memory 仅保证单 pod 防重放。

### FinalizeAuth 生命周期

`Bootstrap.Run` 在所有 `RouteGroups()` 挂载完成后自动调用 primary listener 的 `rtr.FinalizeAuth()`：

1. 收集所有 `auth.Mount` 推送的 `AuthRouteMeta`
2. 去重 `(method, path)` — 重复 fail-fast
3. 校验 `Delegated` ↔ `/internal/v1/*` 一致性（PR-A14a）
4. 编译 public / password-reset-exempt 匹配器
5. 从首个 `POST + PasswordResetExempt=true` 路由派生 password-reset change-endpoint hint
6. AuthMiddleware 在请求时通过 Router 字段 lazy 读取匹配器

### Auth 三路径优先级

每个进入 listener 的请求经历三层鉴权策略，优先级从高到低：

1. **路由级 Policy**（`auth.Route.Policy`）— 仅对该路由生效，覆盖一切
2. **Public / PasswordResetExempt**（`auth.Route.Public: true` 或 `auth.Route.PasswordResetExempt: true`）— 豁免 JWT 验证
3. **Listener 默认 Policy**（`WithListener(ref, addr, defaultPolicy)` 的 `defaultPolicy`）— 对未被 `auth.Mount` 覆盖的路由生效

优先级规则：
- 路由级 Policy 存在时，Listener 默认 Policy 被完全旁路
- `Public: true` 不能与路由级 `Policy` 同时设置（FinalizeAuth fail-fast）
- `Public: true` 是 JWT 豁免标志，只对安装了 JWT 中间件的 listener 有意义；在无 JWT 的 listener（如 HealthListener）上声明 `Public: true` 是无效的（HealthListener 默认配置 `WithSuppressNoAuthVerifierWarn`，Warn 已抑制 — R2-11）
- JWT 单一路径（F3 round-3）：`PolicyJWT(verifier)` 直接注入；`PolicyJWTFromAssembly(asm)` phase4 时通过 `cell.Policy.Validate` 钩子从 `authProvider` Cell 发现 verifier。**没有** `WithAuthMiddleware` / `WithAuthDiscovery` 等 Bootstrap 顶层 Option。Verifier 流向 `router.WithAuthMiddleware`，自动获取 FinalizeAuth 编译的 Public/PasswordResetExempt matcher，零样板。
- `Delegated=true` 路由必须挂在 InternalListener（F2 round-3：FinalizeAuth router-aware 校验，Primary/Health 上 Delegated 直接 fail-fast）。

### 规则

- GET 自动覆盖 HEAD（RFC 7231 §4.3.2）
- `(method, path)` 重复出现 → FinalizeAuth 返回 error（保护配置清洁度）
- `Path` 经过 `path.Clean` 规范化
- Handler 内业务校验优先级高于 Route Policy（如 logout 校验 session 归属）
- CORS OPTIONS：当前无 CORS middleware；如需公开 OPTIONS 请显式 `auth.Mount` + `Public: true`
- 禁止在 `cmd/*` / `examples/*/main.go` 硬编码业务路径字面量（`grep '"POST /api/v1/"'` 必须为空）
- Cell 禁止直接 import `runtime/http/router`；通过 `cell.RouteMux` / `cell.RouteGroup` 声明路由（LAYER-07）
