# /readyz Operations Guide

This page is the operator reference for the `/readyz` readiness endpoint
served by every GoCell binary. PR-A35 reshaped how the endpoint behaves; if
you are carrying a runbook from before that PR some of the commands here
have changed.

## What each endpoint returns

| Path | Default status | Purpose |
|------|----------------|---------|
| `GET /healthz` | 200 | Process-level liveness. Use for Kubernetes `livenessProbe`. Never exposes readiness detail. |
| `GET /readyz` | 200 / 503 | Aggregate readiness across every registered Cell and dependency probe. Use for Kubernetes `readinessProbe` and external LB health checks. |
| `GET /readyz?verbose=true` | 200 / 401 / 503 | Detailed breakdown: cell statuses + per-dependency probe results. Always gated by `X-Readyz-Token` (see below). |

During graceful shutdown `/readyz` returns `503` with
`{"error":{"code":"ERR_READYZ_SHUTTING_DOWN","message":"...","details":{}}}`
so load balancers can drain traffic before the HTTP server closes
connections.

## Response envelope

All PR-A35 health responses use the project-wide JSON envelope
(`.claude/rules/gocell/api-versioning.md`):

- **Success** — `{"data": {...}}` with `status` inside.
- **Error** — `{"error": {"code":"ERR_...", "message":"...", "details":{...}}}`.

The verbose breakdown (cells + dependencies + optional adapters) lives
under `data.*` on 200 and under `error.details.*` on 503, so consumers
walk one consistent path regardless of probe outcome. There is no special
"infrastructure-endpoint" shape to special-case.

## Kubernetes probes — MUST NOT use `?verbose`

Kubernetes only inspects the HTTP status code, so pointing `readinessProbe`
at `/readyz?verbose=true` would pick up the PR-A35 401 denial (when the
token header is missing) and mark healthy pods as NotReady. Always use the
bare path:

```yaml
readinessProbe:
  httpGet:
    path: /readyz          # not /readyz?verbose
    port: 8080
  periodSeconds: 10
  # timeoutSeconds MUST be greater than the handler's probe deadline
  # (bootstrap.WithReadyzDeadline, default 5s) so a slow dependency
  # probe does not cause kubelet to time out its TCP call before the
  # handler has a chance to respond. singleflight makes this ceiling
  # shared across a burst of kubelet + LB + manual probes, so the
  # handler will not be faster than its slowest single probe pass.
  timeoutSeconds: 6
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
  periodSeconds: 10
  timeoutSeconds: 2        # /healthz is cheap — process-level liveness only
```

## Verbose output (debug / on-call)

The verbose body exposes internal topology (cell names, dependency probe
names, optional adapter metadata) and is gated by a bearer-style token in
the `X-Readyz-Token` header.

### Enabling verbose

1. Set the environment variable `GOCELL_READYZ_VERBOSE_TOKEN` to a random
   high-entropy string (treated as a bearer secret — rotate on compromise).
2. Confirm the process logged `controlplane guard` without a verbose-token
   warning.
3. Call the endpoint with the header:

   ```bash
   curl -H "X-Readyz-Token: $GOCELL_READYZ_VERBOSE_TOKEN" \
     "http://$HOST:$PORT/readyz?verbose=true"
   ```

### Response shape

200 (all probes healthy):

```json
{
  "data": {
    "status": "healthy",
    "cells":   { "accesscore": "healthy", "auditcore": "healthy" },
    "dependencies": {
      "postgres-ping": { "status": "healthy", "duration_ms": 3 }
    },
    "adapters": { "storage": "postgres", "eventbus": "rabbitmq" }
  }
}
```

503 (one or more probes unhealthy):

```json
{
  "error": {
    "code": "ERR_READYZ_UNHEALTHY",
    "message": "readiness checks failed",
    "details": {
      "cells": { "accesscore": "healthy", "auditcore": "degraded" },
      "dependencies": {
        "postgres-ping": { "status": "healthy", "duration_ms": 3 },
        "rabbitmq": { "status": "unhealthy", "duration_ms": 12,
                       "error": "connection refused" }
      },
      "adapters": { "storage": "postgres", "eventbus": "rabbitmq" }
    }
  }
}
```

Probe `error` strings are truncated to 512 bytes. Probe implementations
must avoid putting secrets (connection strings, tokens) in their error
messages — this output is intended for operators, not clients.

### Waiving the verbose endpoint

For test harnesses or single-node demos that genuinely do not want the
verbose debug channel at all, set:

```
GOCELL_READYZ_VERBOSE_DISABLED=1
```

When `VerboseDisabled` is in effect, every `?verbose` request is answered
with the plain aggregate body instead of 401. `VerboseDisabled=1` is
rejected in `GOCELL_ADAPTER_MODE=real`: production must retain the
token-gated diagnostic channel.

### Strict 401 semantics

`?verbose` requests are routed like this (the response status here is
independent of the probe outcome — a verbose-authorised 503 still uses the
same error envelope described under "Response shape"):

| Server state | Request | Response |
|--------------|---------|----------|
| `WithVerboseDisabled()` set (e.g. `GOCELL_READYZ_VERBOSE_DISABLED=1`) | any `?verbose` | **200 / 503 plain aggregate body** (no verbose fields) |
| token configured + header matches | `?verbose` with matching `X-Readyz-Token` | **200 / 503 verbose body** |
| token configured + header missing/mismatched | `?verbose` with wrong / no `X-Readyz-Token` | **401** `ERR_READYZ_VERBOSE_DENIED` |
| token unset (and not disabled) — should never happen in prod (Validate refuses startup) | `?verbose` | **401** `ERR_READYZ_VERBOSE_DENIED` |

The 401 body is:

```json
{
  "error": {
    "code": "ERR_READYZ_VERBOSE_DENIED",
    "message": "verbose output requires a matching X-Readyz-Token header",
    "details": {}
  }
}
```

This is stricter than the pre-PR-A35 behaviour (which silently downgraded
mismatched requests to 200) and intentionally so: the old behaviour hid
misconfiguration (operator sets a wrong token → never sees verbose output
but also never sees the failure). Strict 401 surfaces the problem on the
first call. `WithVerboseDisabled()` is the only path that returns 200 for
`?verbose` without a token — it is an explicit operator opt-out, not a
fallback.

## Probe contract

Every checker registered through `health.Handler.RegisterChecker` is
wrapped internally with a race-pattern guard (`wrapCtxSafe`). The outer
Checker is structurally guaranteed to return when the aggregate readyz
deadline fires, regardless of whether the inner probe cooperates with
ctx.Done. This means:

- A well-behaved probe (honours `<-ctx.Done()`) still runs in the
  background after the handler has responded — no change to existing
  correctness.
- A buggy probe that completely ignores ctx will have its inner goroutine
  keep running until its own I/O terminates (usually at TCP/protocol
  timeout). The aggregator is not affected.
- Pathological probes that never terminate (`select{}`, `for{}` with no
  exit) still leak their inner goroutine. These are unit-test bugs, not
  operational problems; run the `healthtest.CheckCtxRespected` helper in your
  probe's own tests to catch them:

  ```go
  func TestMyProbe_RespectsCtx(t *testing.T) {
      healthtest.CheckCtxRespected(t, myProbe, 100*time.Millisecond)
  }
  ```

The runtime no longer imposes a hard-coded time budget on probes —
`CheckCtxRespected`'s budget is caller-supplied and only affects the
developer test, not production behaviour.

## Concurrent probe storms

Concurrent `/readyz` requests (kubelet + LB + manual curl) are
deduplicated via `singleflight`: a burst of N requests is serviced by one
probe execution and N responses share the same aggregate result. There is
no configurable concurrency ceiling; the guarantee is structural, not
throttled.

## Related environment variables

| Variable | Purpose | Required |
|----------|---------|----------|
| `GOCELL_READYZ_VERBOSE_TOKEN` | Bearer token for `?verbose` | Required in every mode unless `GOCELL_READYZ_VERBOSE_DISABLED=1` |
| `GOCELL_READYZ_VERBOSE_DISABLED` | Set to `1` to waive the verbose endpoint | Optional; rejected in adapter mode `real` |
| `GOCELL_METRICS_TOKEN` | Bearer token for `/metrics` | Required in adapter mode `real` |

Refer to `docs/ops/env-vars.md` for the full environment-variable index.
