# R1C-4: runtime/http Module Review

| Field | Value |
|---|---|
| Reviewer Seat | S1 Architecture + S2 Security + S3 Test + S5 DX (combined full-module review) |
| Scope | `runtime/http/` (middleware/, health/, router/) ~650 LOC |
| Review basis commit | `ce03ba1` (HEAD of develop) |
| Date | 2026-04-06 |

---

## Inventory

| File | LOC | Test file | Test LOC |
|---|---|---|---|
| middleware/access_log.go | 43 | middleware/access_log_test.go | 68 |
| middleware/body_limit.go | 39 | middleware/body_limit_test.go | 78 |
| middleware/rate_limit.go | 76 | middleware/rate_limit_test.go | 166 |
| middleware/real_ip.go | 60 | middleware/real_ip_test.go | 116 |
| middleware/recovery.go | 47 | middleware/recovery_test.go | 59 |
| middleware/request_id.go | 52 | middleware/request_id_test.go | 89 |
| middleware/security_headers.go | 16 | middleware/security_headers_test.go | 34 |
| middleware/doc.go | 24 | -- | -- |
| health/health.go | 121 | health/health_test.go | 151 |
| health/doc.go | 5 | -- | -- |
| router/router.go | 165 | router/router_test.go | 142 |
| router/doc.go | 4 | -- | -- |

---

## F-01: RealIP middleware does not support CIDR-based trustedProxies

| Field | Value |
|---|---|
| Seat | S2 Security |
| Severity | **P1** |
| Category | Security / Configuration |
| File | `runtime/http/middleware/real_ip.go:16-18` |
| Status | OPEN |

**Evidence:**

```go
// real_ip.go:16-18
trusted := make(map[string]bool, len(trustedProxies))
for _, p := range trustedProxies {
    trusted[p] = true
}
```

The `trustedProxies` parameter only accepts exact IP addresses. In production deployments behind cloud load balancers (AWS ALB, GCP LB, Kubernetes ingress), proxies often present from a CIDR range (e.g., `10.0.0.0/8`). Without CIDR matching, operators must enumerate every individual proxy IP, which is impractical and error-prone. If the list is incomplete, the middleware falls back to RemoteAddr (the proxy IP), which breaks per-IP rate limiting accuracy.

**Recommendation:**
Parse each entry via `net.ParseCIDR()`; if that fails, treat as a single IP. Match incoming RemoteAddr host against the `[]*net.IPNet` list. This is the standard pattern used by Gin's `SetTrustedProxies` and Echo's `TrustLoopback/TrustPrivateNet`.

---

## F-02: Default router passes `nil` to RealIP -- all forwarding headers ignored in production

| Field | Value |
|---|---|
| Seat | S1 Architecture |
| Severity | **P1** |
| Category | Configuration / Middleware Chain |
| File | `runtime/http/router/router.go:73` |
| Status | OPEN |

**Evidence:**

```go
// router.go:72-79
r.mux.Use(
    middleware.RequestID,
    middleware.RealIP(nil),   // <-- always nil
    middleware.Recovery,
    middleware.AccessLog,
    middleware.SecurityHeaders,
    middleware.BodyLimit(r.bodyLimit),
)
```

`RealIP(nil)` means no proxy is trusted, so `X-Forwarded-For` and `X-Real-Ip` are always ignored. Behind any reverse proxy (nginx, envoy, cloud LB), every request will see the proxy IP as the client IP. This cascades to:
- Rate limiting (F-07) keys all requests to the same proxy IP.
- Access logs record incorrect client IPs.
- Audit trails are inaccurate.

There is no `WithTrustedProxies(...)` router Option to configure this.

**Recommendation:**
Add a `WithTrustedProxies(proxies []string) Option` to the router. The bootstrap layer should read trusted proxies from configuration (e.g., `http.trustedProxies` in YAML). Until configured, warn at startup that RealIP spoofing protection is active but forwarding headers are not trusted.

---

## F-03: No CORS middleware

| Field | Value |
|---|---|
| Seat | S2 Security |
| Severity | **P1** |
| Category | Security / Cross-Origin |
| File | `runtime/http/middleware/` (missing) |
| Status | OPEN |

**Evidence:**
Searched for "CORS" or "cors" across `runtime/` -- zero matches. No `Access-Control-Allow-Origin`, `Access-Control-Allow-Methods`, or `Access-Control-Allow-Headers` are set anywhere in the middleware package.

Any browser-based client (SPA, BFF) calling the GoCell API from a different origin will be blocked by the browser's same-origin policy. This is a fundamental gap for the `sso-bff` example and any real frontend integration.

**Recommendation:**
Add a `CORS` middleware with configurable `AllowedOrigins`, `AllowedMethods`, `AllowedHeaders`, `MaxAge`. Do NOT default to `*` for AllowedOrigins -- require explicit configuration. Consider wrapping `rs/cors` or implementing a lightweight version.

---

## F-04: Missing Content-Security-Policy and Referrer-Policy security headers

| Field | Value |
|---|---|
| Seat | S2 Security |
| Severity | **P2** |
| Category | Security Headers |
| File | `runtime/http/middleware/security_headers.go:9-16` |
| Status | OPEN |

**Evidence:**

```go
// security_headers.go:9-16
func SecurityHeaders(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("X-Content-Type-Options", "nosniff")
        w.Header().Set("X-Frame-Options", "DENY")
        w.Header().Set("Strict-Transport-Security", "max-age=31536000")
        next.ServeHTTP(w, r)
    })
}
```

The following standard security headers are missing:
- `Content-Security-Policy: default-src 'none'` (for API-only service)
- `Referrer-Policy: strict-origin-when-cross-origin`
- `X-Permitted-Cross-Domain-Policies: none`
- `Permissions-Policy: (empty/restrictive)` for API endpoints

The `Strict-Transport-Security` header should include `includeSubDomains` for defence-in-depth.

**Recommendation:**
Add these headers with sensible API defaults. Make CSP configurable via an option for services that also serve HTML.

---

## F-05: statusRecorder in access_log.go duplicates httputil.StatusRecorder

| Field | Value |
|---|---|
| Seat | S5 DX / Maintainability |
| Severity | **P2** |
| Category | Code Duplication |
| Files | `runtime/http/middleware/access_log.go:12-20`, `pkg/httputil/response.go:66-83` |
| Status | OPEN |

**Evidence:**

`access_log.go` defines a private `statusRecorder`:
```go
// access_log.go:12-20
type statusRecorder struct {
    http.ResponseWriter
    status int
}
func (sr *statusRecorder) WriteHeader(code int) {
    sr.status = code
    sr.ResponseWriter.WriteHeader(code)
}
```

`httputil/response.go` defines a public `StatusRecorder` with the same logic:
```go
// response.go:66-83
type StatusRecorder struct {
    http.ResponseWriter
    Status int
}
```

The `runtime/observability/tracing/tracing.go` already uses `httputil.NewStatusRecorder(w)`. The middleware package should reuse the shared type.

**Recommendation:**
Replace the private `statusRecorder` with `httputil.StatusRecorder` / `httputil.NewStatusRecorder()`.

---

## F-06: statusRecorder does not implement Flusher/Hijacker -- breaks SSE and WebSocket upgrade

| Field | Value |
|---|---|
| Seat | S1 Architecture |
| Severity | **P1** |
| Category | Interface Compatibility |
| Files | `runtime/http/middleware/access_log.go:12-20`, `pkg/httputil/response.go:66-83` |
| Status | OPEN |

**Evidence:**

Both `statusRecorder` (middleware) and `StatusRecorder` (httputil) embed `http.ResponseWriter` and override `WriteHeader`, but neither checks for or delegates `http.Flusher`, `http.Hijacker`, or `http.CloseNotifier`.

When the access log middleware wraps the response writer, any downstream handler that type-asserts `w.(http.Flusher)` to flush SSE events will get `false` -- silently breaking Server-Sent Events. Similarly, WebSocket upgrade via `w.(http.Hijacker)` will fail.

This is a known pattern issue documented in the Go standard library.

**Recommendation:**
Implement optional interface delegation. The standard pattern:
```go
func (sr *statusRecorder) Flush() {
    if f, ok := sr.ResponseWriter.(http.Flusher); ok {
        f.Flush()
    }
}
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
    return sr.ResponseWriter
}
```
Go 1.20+ supports `http.ResponseController` which uses `Unwrap()`.

---

## F-07: Rate limiter is per-IP only -- no per-user/per-tenant differentiation

| Field | Value |
|---|---|
| Seat | S2 Security |
| Severity | **P2** |
| Category | Rate Limiting Design |
| File | `runtime/http/middleware/rate_limit.go:37-38` |
| Status | OPEN |

**Evidence:**

```go
// rate_limit.go:37-38
ip := clientIP(r)
if !limiter.Allow(ip) {
```

The rate limiter key is always the client IP. There is no support for per-user or per-tenant rate limiting (using the authenticated subject from context). In multi-tenant deployments or behind NAT, all users from the same IP share the same rate limit bucket. Conversely, an authenticated abuser can rotate IPs to bypass limits.

The `RateLimiter` interface itself (`Allow(key string)`) is flexible enough, but the middleware always passes `clientIP(r)`.

**Recommendation:**
After auth middleware has run, compose a key like `"user:" + subject` when a subject is available, falling back to `"ip:" + clientIP(r)` for unauthenticated requests. This requires rate limiting middleware to be placed after auth in the chain, or to accept a key-extraction function.

---

## F-08: Auth middleware NOT in default router chain -- Cells must manually add it

| Field | Value |
|---|---|
| Seat | S1 Architecture |
| Severity | **P1** |
| Category | Middleware Chain / Security Default |
| File | `runtime/http/router/router.go:72-79` |
| Status | OPEN |

**Evidence:**

The default middleware chain in `router.New()`:
```go
// router.go:72-79
r.mux.Use(
    middleware.RequestID,
    middleware.RealIP(nil),
    middleware.Recovery,
    middleware.AccessLog,
    middleware.SecurityHeaders,
    middleware.BodyLimit(r.bodyLimit),
)
```

Auth middleware (`runtime/auth.AuthMiddleware`) is NOT included. Rate limiting (`middleware.RateLimit`) is also not included by default. The documented expected chain order from the review checklist -- `recovery -> logging -> auth -> rate_limit` -- is not followed.

The current chain order is: `RequestID -> RealIP -> Recovery -> AccessLog -> SecurityHeaders -> BodyLimit`.

While auth is intentionally not in the default chain (it requires a `TokenVerifier` dependency), there is no `WithAuthMiddleware(...)` router option, and the doc.go example in middleware/doc.go shows a chain of `RequestID -> Recovery -> AccessLog` without auth.

**Recommendation:**
1. Add `WithAuthMiddleware(verifier TokenVerifier, publicEndpoints []string) Option` to the router.
2. Add `WithRateLimiter(limiter RateLimiter) Option` to the router.
3. When auth is provided, insert it after AccessLog and before route matching.
4. When rate limiter is provided, insert after RealIP.
5. Document the canonical chain order clearly in doc.go.

---

## F-09: Recovery middleware may write to already-written response

| Field | Value |
|---|---|
| Seat | S1 Architecture |
| Severity | **P2** |
| Category | Correctness |
| File | `runtime/http/middleware/recovery.go:21-46` |
| Status | OPEN |

**Evidence:**

```go
// recovery.go:21-46
defer func() {
    if rec := recover(); rec != nil {
        // ...
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusInternalServerError)
        _ = json.NewEncoder(w).Encode(...)
    }
}()
next.ServeHTTP(w, r)
```

If the downstream handler has already written partial response headers or body bytes before panicking, the recovery middleware's `WriteHeader(500)` call will:
1. Log a superfluous `http: superfluous response.WriteHeader call` warning.
2. Not actually change the status code (first WriteHeader wins).
3. Append JSON error body after whatever was already written, producing malformed output.

**Recommendation:**
Wrap `w` in a `statusRecorder` that tracks whether `WriteHeader` or `Write` has been called. If already written, only log the panic without attempting to write a response body.

---

## F-10: Health check runs readiness checkers sequentially -- no timeout per checker

| Field | Value |
|---|---|
| Seat | S1 Architecture |
| Severity | **P2** |
| Category | Resilience |
| File | `runtime/http/health/health.go:93-99` |
| Status | OPEN |

**Evidence:**

```go
// health.go:93-99
for name, fn := range checkersCopy {
    if err := fn(); err != nil {
        checks[name] = "unhealthy"
        allHealthy = false
    } else {
        checks[name] = "healthy"
    }
}
```

Readiness checkers run sequentially with no timeout. A slow database ping or external service check can block the `/readyz` endpoint indefinitely. Load balancers will mark the instance as unhealthy due to timeout, not due to actual unhealthiness.

Additionally, the `Checker` function signature is `func() error` -- it does not accept a `context.Context`, so callers cannot propagate cancellation or deadlines.

**Recommendation:**
1. Change `Checker` to `func(ctx context.Context) error`.
2. Run checkers with `context.WithTimeout` (e.g., 5 seconds per checker).
3. Consider running checkers concurrently with `errgroup` for latency reduction.

---

## F-11: Middleware error responses do not use `pkg/errcode` or `pkg/httputil`

| Field | Value |
|---|---|
| Seat | S5 DX / Maintainability |
| Severity | **P2** |
| Category | Error Handling Consistency |
| Files | `runtime/http/middleware/rate_limit.go:42-49`, `body_limit.go:31-38`, `recovery.go:35-43` |
| Status | OPEN |

**Evidence:**

Three middleware files construct JSON error responses inline:
```go
// rate_limit.go:42-49
_ = json.NewEncoder(w).Encode(map[string]any{
    "error": map[string]any{
        "code":    "ERR_RATE_LIMITED",
        "message": "too many requests",
    },
})
```

The `auth/middleware.go:130-138` has the same pattern via `writeAuthError()`.

The project has `pkg/httputil.WriteError()` which produces the canonical format with a `details` field:
```go
{"error": {"code": "ERR_*", "message": "...", "details": {}}}
```

The middleware error responses omit the `details` field, creating an inconsistency with the documented error response format in `.claude/rules/gocell/error-handling.md`.

**Recommendation:**
Use `httputil.WriteError(w, status, code, message)` in all middleware error paths. This ensures the `details: {}` field is always present and the format is maintained centrally.

---

## F-12: Error code strings are hardcoded -- not using `pkg/errcode` constants

| Field | Value |
|---|---|
| Seat | S5 DX / Maintainability |
| Severity | **P2** |
| Category | Constant Management |
| Files | `runtime/http/middleware/rate_limit.go:45`, `body_limit.go:35`, `recovery.go:39` |
| Status | OPEN |

**Evidence:**

Error codes are string literals:
- `"ERR_RATE_LIMITED"` (rate_limit.go:45)
- `"ERR_BODY_TOO_LARGE"` (body_limit.go:35)
- `"ERR_INTERNAL"` (recovery.go:39)
- `"ERR_AUTH_UNAUTHORIZED"` (auth/middleware.go:47, 57)
- `"ERR_AUTH_FORBIDDEN"` (auth/middleware.go:113)

The project has a `pkg/errcode` package that should be the single source of truth for error codes. Hardcoded strings risk typos and drift.

**Recommendation:**
Define these codes as `errcode.Code` constants in `pkg/errcode` (e.g., `errcode.ErrRateLimited`, `errcode.ErrBodyTooLarge`) and reference them in middleware.

---

## F-13: `chiRouterAdapter` does not expose `Use()` method

| Field | Value |
|---|---|
| Seat | S1 Architecture |
| Severity | **P2** |
| Category | API Completeness |
| File | `runtime/http/router/router.go:139-165` |
| Status | OPEN |

**Evidence:**

The `Router` type has a `Use()` method (line 125-127) but the `chiRouterAdapter` (used for Group/Route sub-routers) does not. The `RouteMux` kernel interface also does not include `Use()`.

This means Cells calling `mux.Route("/api/v1", func(sub RouteMux) { ... })` cannot add group-level middleware within the sub-router callback. They must cast to a concrete type or use chi directly, which couples Cells to the router implementation.

**Recommendation:**
Consider adding `Use(mw ...func(http.Handler) http.Handler)` to the `RouteMux` interface (kernel). If that is too broad, at minimum add it to `chiRouterAdapter` and create an extended interface `RouteMuxWithMiddleware` that the router can expose.

---

## F-14: `doc.go` example shows incorrect middleware chain order

| Field | Value |
|---|---|
| Seat | S5 DX |
| Severity | **P2** |
| Category | Documentation |
| File | `runtime/http/middleware/doc.go:17-23` |
| Status | OPEN |

**Evidence:**

```go
// doc.go:17-23
//  handler := middleware.RequestID(
//      middleware.Recovery(
//          middleware.AccessLog(
//              myHandler,
//          ),
//      ),
//  )
```

This shows manual nesting (inside-out), but the actual `router.New()` uses `r.mux.Use()` which applies middleware in declaration order (outside-in). The doc.go example also omits `RealIP`, `SecurityHeaders`, and `BodyLimit`, suggesting an incomplete chain.

Furthermore, the nesting approach shown in doc.go applies middleware in the opposite order from `chi.Use()`. A developer reading doc.go and then looking at router.go will be confused.

**Recommendation:**
Update doc.go to show the chi `Use()` pattern or clearly document both patterns with correct ordering.

---

## F-15: Health endpoint response format does not match API envelope convention

| Field | Value |
|---|---|
| Seat | S6 Product/UX |
| Severity | **P2** |
| Category | API Consistency |
| File | `runtime/http/health/health.go:64-68, 109-113` |
| Status | OPEN |

**Evidence:**

Health endpoints return:
```json
{"status": "healthy", "checks": {"cell-1": "healthy"}}
```

The project API convention (from `.claude/rules/gocell/api-versioning.md`) specifies:
```json
{"data": ..., "total": ..., "page": ...}
```

Health/infrastructure endpoints are commonly exempted from the data envelope, but this should be explicitly documented. Also, when unhealthy, the response includes no error details about *why* a checker failed -- just `"unhealthy"`.

**Recommendation:**
1. Document that `/healthz` and `/readyz` are infrastructure endpoints exempt from the data envelope.
2. When a checker returns an error, include the error message in a `reason` field for operational debugging (ensure no sensitive information is leaked).

---

## F-16: Health check `Checker` errors are discarded -- no observability on failure reason

| Field | Value |
|---|---|
| Seat | S5 DX |
| Severity | **P2** |
| Category | Observability |
| File | `runtime/http/health/health.go:93-99` |
| Status | OPEN |

**Evidence:**

```go
// health.go:93-99
for name, fn := range checkersCopy {
    if err := fn(); err != nil {
        checks[name] = "unhealthy"
        allHealthy = false
    } else {
        checks[name] = "healthy"
    }
}
```

When a checker returns an error, the error value is completely discarded. There is no `slog` logging of the error, no metric emission, and no inclusion in the response. Operators will know a component is unhealthy but not why.

**Recommendation:**
At minimum, log the error: `slog.Warn("readiness check failed", slog.String("checker", name), slog.Any("error", err))`. Optionally include a sanitized reason in the JSON response.

---

## F-17: `json.NewEncoder(w).Encode()` errors silently ignored in multiple locations

| Field | Value |
|---|---|
| Seat | S5 DX |
| Severity | **P2** |
| Category | Error Handling |
| Files | `middleware/rate_limit.go:43`, `body_limit.go:33`, `recovery.go:37`, `health/health.go:119` |
| Status | OPEN |

**Evidence:**

All error response writes use `_ = json.NewEncoder(w).Encode(...)`. If the client disconnects mid-write, the error is silently discarded. While this is generally acceptable for HTTP middleware (the connection is already broken), the pattern violates the project's rule in `error-handling.md`:

> Prohibit `_ = someFunc()` ignoring errors; must explicitly handle or log.

**Recommendation:**
At minimum, log the write error at Debug level for the middleware paths (since it's expected for broken connections). For the health endpoint, consider logging at Warn.

---

## F-18: No request timeout middleware

| Field | Value |
|---|---|
| Seat | S2 Security |
| Severity | **P2** |
| Category | Denial of Service |
| File | `runtime/http/middleware/` (missing) |
| Status | OPEN |

**Evidence:**

The bootstrap sets `ReadHeaderTimeout: 10 * time.Second` on the HTTP server (bootstrap.go:279), but there is no request body read timeout or handler execution timeout middleware. A slow-loris attack on the request body or a handler that hangs indefinitely will hold the goroutine forever.

The `http.TimeoutHandler` from the standard library or a custom middleware can enforce per-request execution deadlines.

**Recommendation:**
Add an optional `Timeout(duration time.Duration)` middleware that wraps `http.TimeoutHandler`. Apply it before business handlers in the chain. Set a sensible default (e.g., 30 seconds) with configurability.

---

## GoCell Layer Dependency Check

| Rule | Status | Notes |
|---|---|---|
| kernel/ must not import runtime/adapters/cells/ | PASS | `kernel/cell/registrar.go` defines `RouteMux` with only `net/http` + `kernel/outbox` imports |
| cells/ must not import adapters/ | N/A | No cells/ code in scope |
| runtime/ must not import cells/ or adapters/ | PASS | `router/router.go` imports `kernel/cell`, `runtime/http/*`, `runtime/observability/*` -- all allowed |
| Cross-Cell import prohibition | N/A | No cross-cell imports in scope |

---

## Test Coverage Assessment

| Component | Test Exists | Edge Cases Covered | Assessment |
|---|---|---|---|
| access_log | Yes | Default status, request_id propagation | Adequate |
| body_limit | Yes | Under/over limit, unknown content-length, default | Good |
| rate_limit | Yes | Allow/reject, windowed/non-windowed, fallback IP, ceil rounding | Good |
| real_ip | Yes | Trusted/untrusted, XFF chain, X-Real-Ip, fallback | Good |
| recovery | Yes | No panic, string panic, int panic | Missing: panic after partial write |
| request_id | Yes | Existing header, generation, too-long, control chars, uniqueness | Good |
| security_headers | Yes | All three headers verified | Adequate |
| health | Yes | Healthy, unhealthy cell, unhealthy checker, empty assembly | Missing: concurrent checker registration |
| router | Yes | RouteMux interface, health, metrics, Handle, Route, Group, Mount, default middleware | Missing: auth integration, body limit trigger |

**Overall:** Test coverage appears solid for happy paths and basic error cases. The main gaps are:
- No test for recovery middleware behavior when response is already partially written (relates to F-09).
- No test for health checker timeout behavior (relates to F-10).
- No concurrent safety test for `health.RegisterChecker` during serving.

---

## Summary

| Severity | Count | Finding IDs |
|---|---|---|
| P0 | 0 | -- |
| P1 | 4 | F-01, F-02, F-03, F-06, F-08 |
| P2 | 13 | F-04, F-05, F-07, F-09, F-10, F-11, F-12, F-13, F-14, F-15, F-16, F-17, F-18 |

**Note:** P1 count is 5 (F-01, F-02, F-03, F-06, F-08). No P0 blockers found.

The runtime/http module is well-structured with clean separation of concerns. Each middleware follows the standard `func(http.Handler) http.Handler` pattern and the router correctly implements the `kernel/cell.RouteMux` interface. Test coverage is thorough for the code that exists.

The primary gaps are in production-readiness configuration: the RealIP middleware cannot be properly configured via router options (F-02), CIDR matching is missing (F-01), CORS is absent (F-03), and auth/rate-limit are not wired into the default chain (F-08). The `statusRecorder` interface compatibility issue (F-06) will surface as soon as SSE or WebSocket endpoints are added.
