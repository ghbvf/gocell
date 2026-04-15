---
paths:
  - "runtime/**/*.go"
  - "cmd/**/*.go"
  - "examples/**/*.go"
---

# Runtime API

## Auth 接线

用 `WithPublicEndpoints`，不用 `WithAuthMiddleware`（deprecated）。

```go
bootstrap.New(
    bootstrap.WithAssembly(asm),
    bootstrap.WithPublicEndpoints([]string{"/api/v1/auth/login", "/api/v1/auth/refresh"}),
)
```
