---
paths:
  - "runtime/**/*.go"
  - "cmd/**/*.go"
  - "examples/**/*.go"
  - "cells/**/*.go"
---

# Runtime API

## Auth 路由声明 (F3)

每个 Cell 通过 `auth.Declare(mux, auth.RouteDecl{...})` 注册路由，在注册点声明
鉴权语义。composition root 只需 `bootstrap.WithAuthDiscovery()` 启用鉴权验证器发现，
或 `bootstrap.WithAuthMiddleware(verifier)` 直接注入测试用 verifier。

```go
// Cell.RegisterRoutes
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
    mux.Route("/api/v1/access", func(sub cell.RouteMux) {
        sub.Route("/sessions", func(s cell.RouteMux) {
            auth.Declare(s, auth.RouteDecl{
                Method:  "POST",
                Path:    "/login",
                Handler: http.HandlerFunc(c.loginHandler.HandleLogin),
                Public:  true,                     // JWT 豁免
            })
            auth.Declare(s, auth.RouteDecl{
                Method:              "DELETE",
                Path:                "/{id}",
                Handler:             http.HandlerFunc(c.logoutHandler.HandleLogout),
                PasswordResetExempt: true,         // 允许 reset-required token 穿过
            })
        })
    })
}

// composition root
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithAuthDiscovery(),  // 从 Cell 发现 IntentTokenVerifier
)
```

### RouteDecl 字段

| 字段 | 说明 | 约束 |
|------|------|------|
| `Method` | HTTP 动词（GET/POST/...） | 必填，大写 |
| `Path` | 路径（相对当前 mux.Route 作用域） | 必填，以 `/` 开头 |
| `Handler` | `http.Handler` | 必填，非 nil |
| `Policy` | `auth.Policy` — 路由级策略 | 可选；`Public=true` 时必须为 nil |
| `Public` | JWT 豁免 | 与 `Policy` / `PasswordResetExempt` 互斥 |
| `PasswordResetExempt` | 允许 password-reset token | 与 `Public` 互斥；handler 内做细粒度校验 |
| `Delegated` | JWT 验证下放（service-token / mTLS） | 通常配合 `WithInternalPathPrefixGuard` |

### FinalizeAuth 生命周期

`Bootstrap.Run` 在 `Cell.RegisterRoutes` 完成后自动调用 `rtr.FinalizeAuth()`：

1. 收集所有 `auth.Declare` 推送的 `AuthRouteMeta`
2. 去重 `(method, path)` — 重复 fail-fast
3. 编译 public / password-reset-exempt / delegated 匹配器
4. 从首个 `POST + PasswordResetExempt=true` 路由派生 password-reset change-endpoint hint
5. AuthMiddleware 在请求时通过 Router 字段 lazy 读取匹配器

### 规则

- GET 自动覆盖 HEAD（RFC 7231 §4.3.2）
- `(method, path)` 重复出现 → FinalizeAuth 返回 error（保护配置清洁度）
- `Path` 经过 `path.Clean` 规范化
- Handler 内业务校验优先级高于 Route Policy（如 logout 校验 session 归属）
- CORS OPTIONS：当前无 CORS middleware；如需公开 OPTIONS 请显式 `auth.Declare` + `Public: true`
- 禁止在 `cmd/*` / `examples/*/main.go` 硬编码业务路径字面量（`grep '"POST /api/v1/"'` 必须为空）
