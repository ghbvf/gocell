# runtime/ 层规则

runtime/ 提供通用运行时能力：`http` / `auth` / `bootstrap` / `eventrouter` / `worker` / `observability` / `crypto`。

## 依赖约束

**允许**：`kernel/` + `pkg/`
**严禁**：`cells/`、`adapters/`

## Auth 路由声明（F3）

每个 Cell 通过 `auth.Mount(mux, auth.Route{...})` 注册路由，在注册点声明鉴权语义。
`auth.Route.Contract` 必填，HTTP method/path 从 `wrapper.ContractSpec` 读取。

```go
func (c *MyCell) RegisterRoutes(mux cell.RouteMux) {
    mux.Route("/api/v1/access", func(sub cell.RouteMux) {
        sub.Route("/sessions", func(s cell.RouteMux) {
            auth.Mount(s, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.access.sessions.login.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "POST",
                    Path:      "/api/v1/access/sessions/login",
                },
                Handler: http.HandlerFunc(c.loginHandler.HandleLogin),
                Public:  true, // JWT 豁免
            })
            auth.Mount(s, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.access.sessions.logout.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "DELETE",
                    Path:      "/api/v1/access/sessions/{id}",
                },
                Handler:             http.HandlerFunc(c.logoutHandler.HandleLogout),
                PasswordResetExempt: true, // 允许 reset-required token 穿过
            })
        })
    })
}
```

### Route 字段

| 字段 | 说明 | 约束 |
|------|------|------|
| `Contract` | `wrapper.ContractSpec`——contract id、HTTP method/path、transport metadata | 必填；`Kind="http"`，`Method` 大写，`Path` 为全路径 |
| `Handler` | `http.Handler` | 必填，非 nil |
| `Policy` | `auth.Policy`——路由级策略 | 可选；`Public=true` 时必须为 nil |
| `Public` | JWT 豁免 | 与 `Policy` / `PasswordResetExempt` 互斥 |
| `PasswordResetExempt` | 允许 password-reset token 穿过 | 与 `Public` 互斥；handler 内做细粒度校验 |
| `Delegated` | JWT 验证下放（service-token / mTLS） | 配合 `WithInternalPathPrefixGuard` |

### FinalizeAuth 生命周期

`Bootstrap.Run` 在 `Cell.RegisterRoutes` 完成后自动调用 `rtr.FinalizeAuth()`：

1. 收集所有 `auth.Mount` 推送的 `AuthRouteMeta`
2. 去重 `(method, path)`——重复 fail-fast
3. 编译 public / password-reset-exempt / delegated 匹配器
4. 从首个 `POST + PasswordResetExempt=true` 路由派生 password-reset change-endpoint hint
5. AuthMiddleware 在请求时通过 Router 字段 lazy 读取匹配器

### 规则

- GET 自动覆盖 HEAD（RFC 7231 §4.3.2）
- `(method, path)` 重复出现 → FinalizeAuth 返回 error，保护配置清洁度
- `Path` 经过 `path.Clean` 规范化
- **禁止**在 `cmd/*/main.go` 或 `examples/*/main.go` 硬编码业务路径字面量（`grep '"POST /api/v1/"'` 必须为空）
- CORS OPTIONS：当前无 CORS middleware；如需公开 OPTIONS，显式 `auth.Mount` + `Public: true`

## Composition Root（Bootstrap）

```go
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithAuthDiscovery(),     // 从 Cell 发现 IntentTokenVerifier
    // 或 bootstrap.WithAuthMiddleware(verifier) 直接注入（测试用）
)
```

- `WithAuthDiscovery()` 用于生产：自动从注册的 Cell 发现 verifier
- `WithAuthMiddleware(verifier)` 用于测试：直接注入 mock verifier
- 两者互斥，composition root 只选其一

## EventRouter 订阅注册

Cell 在 `RegisterSubscriptions` 中通过 `r.AddContractHandler(spec, handler, consumerGroup)` 声明订阅意图。
**禁止**手动启动 goroutine 或调用 `sub.Subscribe`——Router 管理所有 goroutine 生命周期。

```go
func (c *MyCell) RegisterSubscriptions(r cell.EventRouter) error {
    handler := outbox.WrapLegacyHandler(c.svc.HandleEvent)
    r.AddContractHandler(wrapper.ContractSpec{
        ID:        "event.my.topic.v1",
        Kind:      "event",
        Transport: "amqp",
        Topic:     "my.topic.v1",
    }, handler, c.ID())
    return nil
}
```

旧签名迁移：

```go
legacy := func(ctx context.Context, entry outbox.Entry) error { ... }
handler := outbox.WrapLegacyHandler(legacy)
// nil error → Ack, PermanentError → Reject, other error → Requeue
```
