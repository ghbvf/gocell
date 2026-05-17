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
`{"error":{"code":"ERR_SERVICE_UNAVAILABLE","message":"service unavailable","details":[]}}`
so load balancers can drain traffic before the HTTP server closes
connections. The shutdown reason is emitted server-side as a structured
`slog.Info("readyz: shutting down (graceful_shutdown)", status="shutting_down", reason="graceful_shutdown")`
record — operators correlate via logs, not the public wire body.

## Response envelope

All health responses use the project-wide JSON envelope
(`.claude/rules/gocell/api-versioning.md`):

- **Success** — `{"data": {...}}` with `status` inside.
- **Error** — `{"error": {"code":"ERR_...", "message":"...", "details":[]}}`. The
  `details` field is an `array<{key,value}>` per the shared envelope
  (`contracts/shared/errors/error-response-v1.schema.json`); 5xx responses
  always emit an empty array (K#08 5xx redaction policy — runtime context
  never reaches public clients).

The verbose breakdown (cells + dependencies + optional adapters) lives
under `data.*` on 200 only. On 503 the wire body carries no breakdown; the
same data is emitted to server-side `slog`
(`logger.Warn("readyz unhealthy", status, reason, cells, dependencies, adapters)`)
so on-call retains the diagnostic without leaking it to public 503
consumers. ref: k8s.io/apiserver/pkg/server/healthz — failed checks do not
surface in the 503 body; verbose breakdown is operator-only.

Public `/readyz` 503 reasons are intentionally low-cardinality. Operators
read them from the structured `slog` record (the wire body carries an empty
details array):

| slog level | slog `status` | slog `reason` | Meaning |
|------------|---------------|---------------|---------|
| `Warn` (msg=`readyz unhealthy`) | `unhealthy` | `readiness_failed` | One or more cells/probes failed, or the readiness aggregator failed closed. Internal computation failures are logged server-side and do not create a separate public reason. |
| `Info` (msg=`readyz: shutting down (graceful_shutdown)`) | `shutting_down` | `graceful_shutdown` | The process is draining and should be removed from load balancer traffic. |

On-call dashboards / alert rules that filter by level alone will miss the
`shutting_down` path; query both `level=Warn AND msg="readyz unhealthy"`
and `level=Info AND status="shutting_down"` to capture every 503.

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
	      "postgres_ready": { "status": "healthy", "duration_ms": 3 }
	    },
	    "adapters": { "storage": "postgres", "eventbus": "rabbitmq" }
	  }
}
```

503 (one or more probes unhealthy):

```json
{
  "error": {
    "code": "ERR_SERVICE_UNAVAILABLE",
    "message": "service unavailable",
    "details": []
  }
}
```

Even with `?verbose=true`, the 503 wire body always carries an empty
`details` array (K#08 5xx strip — public clients never see runtime
context). The breakdown depth in the slog record depends on whether the
triggering request was verbose:

- **Non-verbose 503** (kubelet probe / unauthenticated `/readyz` hit):
  slog record carries only `status` + `reason`. cells / dependencies /
  adapters fields are not appended.
- **Verbose 503** (request carries a matching `X-Readyz-Token` and
  `?verbose=true`): slog record additionally carries cells +
  dependencies + adapters maps.

Verbose 503 slog example（text handler，`-log-format=text` 默认）：

```
level=WARN msg="readyz unhealthy"
  status=unhealthy reason=readiness_failed
  cells=map[accesscore:healthy auditcore:degraded]
  dependencies.postgres_ready.status=healthy
  dependencies.postgres_ready.duration_ms=3
  dependencies.postgres_ready.error_msg=""
  dependencies.rabbitmq_ready.status=unhealthy
  dependencies.rabbitmq_ready.duration_ms=12
  dependencies.rabbitmq_ready.error_msg="<REDACTED>"
  adapters=map[storage:postgres eventbus:rabbitmq]
```

Verbose 503 slog example（JSON handler，`-log-format=json`）：

```json
{
  "level": "WARN",
  "msg": "readyz unhealthy",
  "status": "unhealthy",
  "reason": "readiness_failed",
  "cells": {"accesscore": "healthy", "auditcore": "degraded"},
  "dependencies": {
    "postgres_ready": {"status": "healthy", "duration_ms": 3, "error_msg": ""},
    "rabbitmq_ready": {"status": "unhealthy", "duration_ms": 12, "error_msg": "<REDACTED>"}
  },
  "adapters": {"storage": "postgres", "eventbus": "rabbitmq"}
}
```

`dependencies` 字段是 `slog.Group` 而非 `slog.Any(map)`——Group 内每个 sub-attr 的 value
是 `health.SlogDependencyEntry`（LogValuer），handler 在 Resolve 阶段调
`SlogDependencyEntry.LogValue()` → `slog.GroupValue(status / duration_ms / error_msg)`。
所有 handler（JSON / text / logfmt）输出一致的 snake_case 字段；不会因 unexported
字段而退化到 `{}`（这是 round-4 实测 bug 的形态，round-5 改 `slog.Group` 后修复）。

Operators who need the full breakdown for an outage correlate 503s with
the structured slog record via the standard log pipeline; if the triggering
probes were non-verbose, hit `/readyz?verbose=true` manually with the
operator token to elicit a verbose record.

`dependencies[*].error` 字段在 ADR 202605171200 后已从 wire 响应体中完全移除。
slog 通道（ops-diagnostics 通道 d）使用 typed `SlogDependencyEntry`，其 `errorMsg`
字段（slog 序列化为 `error_msg`）经 `pkg/redaction.RedactString` 脱敏后才写入
slog——脱敏后的字符串不再截断（slog 落盘容量不是问题；截断只在 wire 才必要，wire 不携带
error 文本就无需截断）。Probe 实现仍应避免在 error message 中硬编码裸 secret，作为
纵深防御。

## 操作员诊断 cookbook

### JSON handler（`-log-format=json`）

```bash
# 过滤所有 readyz unhealthy 事件并展示 dependencies 字段
kubectl logs <pod> | jq 'select(.msg == "readyz unhealthy") | .dependencies'

# 进一步定位某个 probe 的 error_msg
kubectl logs <pod> | jq 'select(.msg == "readyz unhealthy") | .dependencies.rabbitmq_ready.error_msg'

# Grafana / Loki LogQL 查询示例（结构化字段索引）：
# {app="my-app"} | json | msg = "readyz unhealthy" | dependencies_rabbitmq_ready_status = "unhealthy"
```

### Text handler（`-log-format=text`，默认）

```bash
# 过滤 readyz unhealthy 行
kubectl logs <pod> | grep "readyz unhealthy"

# 提取某个 probe 的 error_msg（key=value 形态）
kubectl logs <pod> | grep "readyz unhealthy" | grep -oE 'dependencies\.rabbitmq_ready\.error_msg="[^"]*"'
```

text handler 输出形态是 `dependencies.<probe_name>.<field>=<value>` 而非嵌套 `{}` 块，
Loki / Grafana 通过 key=value 解析直接索引。如需更结构化的查询能力（嵌套对象路径），
切换到 JSON handler。

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
    "details": []
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

## Cell-level repo readiness probes

In addition to the adapter-level `postgres_ready` probe (a bare pool Ping), each
platform Cell registers a cell-level repo readiness probe via
`cell.RegisterRepoReadiness`. These probes represent a **distinct failure domain**:
they execute a representative query against the Cell's own relation(s), surfacing
schema/migration drift, missing tables, and table-level permission loss that a
connection Ping cannot detect.

| Probe name | Owning Cell | Probed relation(s) | Backend |
|---|---|---|---|
| `config_repo_ready` | configcore | `config_entries`, `feature_flags` | PG only; mem stores always return nil (ready) |
| `session_store_ready` | accesscore | `sessions` | PG only; mem stores always return nil (ready) |
| `audit_ledger_ready` | auditcore | `audit_entries` (via `Tail`) | PG only; mem stores always return nil (ready) |

These probes are **not synonymous** with `postgres_ready`. A green `postgres_ready`
and a failing `session_store_ready` means the PG connection is alive but the
`sessions` table is inaccessible — a different remediation path (migration replay,
permission grant) than a connection failure. Operators must monitor both probe
families independently.

Cell-level repo probe names appear in the verbose breakdown under `dependencies`:

```json
"dependencies": {
  "postgres_ready":      { "status": "healthy", "duration_ms": 3 },
  "config_repo_ready":   { "status": "healthy", "duration_ms": 2 },
  "session_store_ready": { "status": "healthy", "duration_ms": 1 },
  "audit_ledger_ready":  { "status": "healthy", "duration_ms": 2 }
}
```

ref: `docs/architecture/202605161030-adr-cell-repo-readyz-probe.md` §D1 — differentiated
failure domain rationale; `.claude/rules/gocell/observability.md` §Cell 级别 Repo
Readiness Probe.

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
