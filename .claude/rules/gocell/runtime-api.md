---
paths:
  - "runtime/**/*.go"
  - "cmd/**/*.go"
  - "examples/**/*.go"
---

# Runtime API

## Auth 接线

用 `WithPublicEndpoints`，不用 `WithAuthMiddleware`（deprecated）。

每条目必须是 `"METHOD /path"` 格式（Go 1.22 ServeMux 语法对齐）。无 METHOD 前缀的条目在启动时 fail-fast。

```go
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithPublicEndpoints([]string{
        "POST /api/v1/auth/login",
        "POST /api/v1/auth/refresh",
    }),
)
```

规则：
- GET 条目自动覆盖 HEAD（RFC 7231 §4.3.2，对齐 stdlib ServeMux + chi v5）
- 同一 `(method, path)` 对重复出现 → 启动 fail-fast（保护配置清洁度）
- path 经过 `path.Clean` 规范化，`/foo/` 和 `/foo` 等价
- CORS OPTIONS 预检：当前无 CORS middleware；如需公开 OPTIONS 请显式声明
