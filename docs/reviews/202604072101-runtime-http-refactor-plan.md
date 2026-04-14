# Runtime HTTP Refactor Plan

> Date: 2026-04-07 21:01
> Status: Draft
> Scope: `pkg/errcode`, `pkg/httputil`, `runtime/http/*`, `runtime/observability/*`, HTTP-facing cells

## 1. Goals

This plan captures the agreed direction after the pkg review and the follow-up runtime HTTP analysis:

1. Fix the concrete pkg bug: `errcode.WithDetails(nil, ...)` must not panic.
2. Remove duplicated HTTP machinery that chi or the Go standard library already provides.
3. Keep only framework-specific HTTP semantics that are truly part of GoCell's contract.
4. Preserve WebSocket upgrade correctness during and after the refactor.

## 2. Decision Rules

Use these rules to decide whether code stays, moves, or is deleted:

1. `pkg/` must remain `chi`-free.
2. `cells/` must not import `chi`.
3. Mechanical HTTP plumbing should be delegated to `chi` or the standard library.
4. GoCell should keep only semantic behavior that is part of its framework contract:
   - canonical JSON error envelope
   - context correlation fields used across subsystems
   - rate-limit behavior and response shape
   - optional trusted-proxy policy, only if fully wired into runtime config

## 3. Immediate Fixes

### 3.1 `errcode.WithDetails(nil, ...)`

Problem:
- `WithDetails` dereferences `err.Details` unconditionally and panics on nil input.

Plan:
1. Change `WithDetails(err *Error, details map[string]any) *Error` to return `nil` when `err == nil`.
2. Keep the existing non-mutating merge behavior for non-nil errors.
3. Add tests for:
   - nil error returns nil
   - nil details does not panic
   - original error remains unchanged

Acceptance:
- No panic on nil input.
- Existing tests still pass.

## 4. Target Architecture After Refactor

### 4.1 Keep in `pkg/`

Keep these as framework/shared contracts:
- `pkg/errcode`
- `pkg/ctxkeys` for cross-cutting correlation values
- `pkg/httputil` only for JSON/error response helpers

Do not keep in `pkg/`:
- `StatusRecorder`
- route-param helpers that depend on `chi`
- any `chi`-specific response writer wrapper

### 4.2 Keep in `runtime/http`

Keep these as runtime semantics:
- `Recovery`
- `RateLimit`
- `SecurityHeaders`
- router abstraction via `kernel/cell.RouteMux`

### 4.3 Replace or Thin Down

Replace duplicated mechanical pieces:

1. `StatusRecorder`
   - delete custom wrapper logic
   - use `chi/middleware.NewWrapResponseWriter`
   - keep only the minimal status/bytes accessor surface needed by GoCell code

2. `access_log` local recorder
   - delete the second custom recorder
   - use the same chi-based writer wrapper as metrics/tracing

3. `RequestID`
   - stop owning UUID generation logic
   - use `chi/middleware.RequestID`
   - add a thin bridge that:
     - copies the request ID into `ctxkeys.RequestID`
     - mirrors the value to `X-Request-Id` response header

4. route params in cells
   - replace `chi.URLParam(r, "...")` with `r.PathValue("...")`
   - remove direct `chi` imports from cells

## 5. Capability Review: Keep vs Remove

### 5.1 Keep

These are still worth owning:

1. Canonical error envelope
   - `WriteError`
   - `WriteDomainError`
   - panic recovery response shape

2. Cross-subsystem correlation keys
   - `request_id`
   - `trace_id`
   - `span_id`
   - `subject`

3. Rate-limit semantics
   - `429`
   - `Retry-After`
   - GoCell error payload
   - pluggable limiter interface

### 5.2 Remove or Stop Treating as Special

These do not justify long-term custom ownership:

1. custom request ID generation format
2. custom response writer wrappers
3. direct `chi.URLParam` access in cells

### 5.3 Re-evaluate

`RealIP` / `trustedProxies` needs a conscious product decision.

Current state:
- capability exists in code
- default router still uses `RealIP(nil)`
- runtime config does not appear to fully drive the trusted-proxy chain

Decision gate:
1. If GoCell wants trusted-proxy security as a real supported feature:
   - keep a custom `RealIP`
   - add router/bootstrap/config support
   - add startup wiring and tests for configured proxies
2. If not:
   - remove custom `RealIP`
   - use chi's `RealIP`
   - treat proxy trust as deployment-level responsibility

Until this decision is made, do not expand the current half-integrated implementation.

## 6. WebSocket Guardrail

WebSocket is the main correctness gate for this refactor.

Reason:
- `adapters/websocket.UpgradeHandler` ultimately calls `websocket.Accept`
- upgrade requires `http.Hijacker`
- any incorrect `ResponseWriter` wrapper can break upgrade

Required rule:
- every HTTP writer wrapper used by access log, tracing, or metrics must preserve optional interfaces

Required regression coverage:
1. `router.New()` + default middleware chain + mounted WebSocket upgrade handler
2. WebSocket handshake succeeds
3. message exchange still works after upgrade
4. metrics/tracing wrapping does not break upgrade

## 7. Execution Plan

### Phase A: Safe Bug Fixes

1. Fix `WithDetails(nil, ...)`
2. Add missing `ctxkeys` tests for `RequestID`, `RealIP`, `Subject`

### Phase B: ResponseWriter Consolidation

1. Introduce one runtime-local chi-based writer wrapper
2. Migrate:
   - `runtime/http/middleware/access_log.go`
   - `runtime/observability/metrics/metrics.go`
   - `runtime/observability/tracing/tracing.go`
3. Delete custom recorder implementations

### Phase C: Request/Route Simplification

1. Replace custom `RequestID` generation with chi + bridge
2. Replace all cell-level `chi.URLParam` use with `r.PathValue`
3. remove direct `chi` imports from cells

### Phase D: RealIP Decision

Pick one path:
- full trusted-proxy productization, or
- revert to chi behavior and simplify

Do not leave this in mixed state.

## 8. Acceptance Checklist

- `errcode.WithDetails(nil, ...)` no longer panics
- `pkg/` does not import `chi`
- `cells/` do not import `chi`
- only one response-writer wrapping strategy exists
- WebSocket upgrade works through the router/middleware chain
- logs still include `request_id`
- rate-limit behavior and JSON error envelopes remain unchanged

## 9. Suggested PR Split

1. PR 1: `errcode` nil-safety + test coverage cleanup
2. PR 2: response-writer consolidation (`access_log`, `metrics`, `tracing`)
3. PR 3: request-id bridge + route-param cleanup
4. PR 4: `RealIP` final decision and wiring
