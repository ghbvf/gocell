# runtime/ 层规则

runtime/ 提供通用运行时能力：`http` / `auth` / `bootstrap` / `eventrouter` / `worker` / `observability` / `crypto`。

## 依赖约束

**允许**：`kernel/` + `pkg/`
**严禁**：`cells/`、`adapters/`

## Auth 路由声明（F3）

每个 Cell 通过 `RouteGroups() []cell.RouteGroup` 声明物理 listener 和路径前缀，
并在每个 `RouteGroup.Register` 闭包里调用 `auth.Mount(mux, auth.Route{...})`
注册路由。`auth.Route.Contract` 必填，HTTP method/path 从
`wrapper.ContractSpec` 读取。

```go
func (c *MyCell) RouteGroups() []cell.RouteGroup {
    return []cell.RouteGroup{
        cell.SingleGroup(cell.PrimaryListener, "/api/v1/access/sessions", func(mux cell.RouteMux) error {
            if err := auth.Mount(mux, auth.Route{
                Contract: wrapper.ContractSpec{
                    ID:        "http.access.sessions.login.v1",
                    Kind:      "http",
                    Transport: "http",
                    Method:    "POST",
                    Path:      "/api/v1/access/sessions/login",
                },
                Handler: http.HandlerFunc(c.loginHandler.HandleLogin),
                Public:  true, // JWT 豁免
            }); err != nil {
                return err
            }
            return auth.Mount(mux, auth.Route{
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
        }),
    }
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

### FinalizeAuth 生命周期

`Bootstrap.Run` 在所有 `RouteGroup.Register` 闭包完成后自动调用
`rtr.FinalizeAuth()`：

1. 收集所有 `auth.Mount` 推送的 `AuthRouteMeta`
2. 去重 `(method, path)`——重复 fail-fast
3. 编译 public / password-reset-exempt 匹配器
4. 从首个 `POST + PasswordResetExempt=true` 路由派生 password-reset change-endpoint hint
5. 校验 `/internal/v1/*` 路由只能挂在 `cell.InternalListener`
6. AuthMiddleware 在请求时通过 Router 字段 lazy 读取匹配器

### 规则

- GET 自动覆盖 HEAD（RFC 7231 §4.3.2）
- `(method, path)` 重复出现 → FinalizeAuth 返回 error，保护配置清洁度
- `Path` 经过 `path.Clean` 规范化
- **禁止**在 `cmd/*/main.go` 或 `examples/*/main.go` 硬编码业务路径字面量（`grep '"POST /api/v1/"'` 必须为空）
- CORS OPTIONS：当前无 CORS middleware；如需公开 OPTIONS，显式 `auth.Mount` + `Public: true`

## Composition Root（Bootstrap）

JWT auth 通过 listener 的 `[]cell.ListenerAuth` 装配；RouteGroup 继承 listener auth chain。

```go
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithListener(cell.PrimaryListener, ":8080",
        []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}),
    bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9090",
        []cell.ListenerAuth{cell.MustNewAuthServiceToken(store, ring)}),
    bootstrap.WithListener(cell.HealthListener, ":9091", // PodIP/Service reachable
        []cell.ListenerAuth{cell.AuthNone{}}),
)
```

- `cell.MustNewAuthJWTFromAssembly(asm)` 用于生产：phase4 通过 listener auth plan 从 `authProvider` Cell 发现 verifier
- `cell.MustNewAuthJWT(verifier)` 用于测试或非 assembly-discovery 场景：直接注入
- 单一路径：verifier 经 listener auth plan 流向 `router.WithAuthMiddleware`，自动获取 FinalizeAuth 编译的 Public/PasswordResetExempt matcher

## EventRouter 订阅注册

Cell 在 `RegisterSubscriptions` 中通过 `r.AddContractHandler(spec, handler, consumerGroup)` 声明订阅意图。
**禁止**手动启动 goroutine 或调用 `sub.Subscribe`——Router 管理所有 goroutine 生命周期。

```go
func (c *MyCell) RegisterSubscriptions(r cell.EventRouter) error {
    // c.svc.HandleEvent 直接实现 outbox.EntryHandler — 业务返回
    // outbox.HandleResult{Disposition: Ack/Requeue/Reject}（029 #03 ADR
    // Decision 1：WrapLegacyHandler 已删除，没有 error→Disposition 适配层）。
    r.AddContractHandler(wrapper.ContractSpec{
        ID:        "event.my.topic.v1",
        Kind:      "event",
        Transport: "amqp",
        Topic:     "my.topic.v1",
    }, c.svc.HandleEvent, c.ID())
    return nil
}
```
