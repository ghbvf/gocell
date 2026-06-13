# /readyz Operations Guide

This page is the operator reference for the `/readyz` readiness endpoint
served by every GoCell binary. PR-A35 reshaped how the endpoint behaves; if
you are carrying a runbook from before that PR some of the commands here
have changed.

## Recent breaking changes

### PR #1187 (round-3) — outbox relay probe wire key rename

The three outbox relay probes had their `/readyz?verbose` `dependencies[].name`
field values renamed from hyphen-form to underscore-form as part of the
`ProbeName` typed funnel rollout (`probeNamePattern` regex prohibits hyphens):

| Old wire key (pre-#1187)  | New wire key (#1187+)     |
|---------------------------|---------------------------|
| `outbox-relay-poll`       | `outbox_relay_poll`       |
| `outbox-relay-reclaim`    | `outbox_relay_reclaim`    |
| `outbox-relay-cleanup`    | `outbox_relay_cleanup`    |

Downstream consumers — Prometheus rules, Grafana dashboards, alerting rules,
startup-validation scripts, runbook commands — that hard-coded the hyphen
form must be updated. Within this repository, no active operational
configuration (Prometheus rules / Grafana dashboards / alerting YAML / ops
scripts) references the old hyphen form (verified by
`grep -rn 'outbox-relay-' --include='*.yaml' --include='*.json'` returning
zero hits across the tree). Historical ADRs / docs / archived plans may
still mention the old hyphen form for archival traceability; those are not
load-bearing and have been cross-referenced. Recommended deployment order:
update monitoring configuration **first**, then roll out the new binary.

See ADR `docs/architecture/202605271100-adr-probename-sealed-funnel.md` F1
amendment §3 for full background.

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
under `data.*` on 200 (healthy **and** degraded) only. On 503 the wire body
carries no breakdown; the same data is emitted to server-side `slog` via
`slog.Log(ctx, level, "readyz <status>", ...)` — a `slog.Group("dependencies",
...)` plus status / reason / cells / adapters attrs — so on-call retains the
diagnostic without leaking it to public 503 consumers. Because `slog.Log`
receives the **request context**, a readyz record carries the framework
correlation fields the contextHandler injects from ctx — the same source as the
errcode `WithInternal` path. On probe endpoints those are **`request_id` +
`correlation_id`** (the RequestID middleware runs on `/readyz` and is not
probe-filtered), so a 503/degraded record can be joined back to the request
that produced it (#942). **`trace_id` is NOT emitted on probe endpoints by
default** — the Tracing middleware's `DefaultProbeFilter` skips span creation
for `/healthz`, `/readyz`, `/livez`, `/metrics`; it only appears if a
deployment removes that filter. ref: k8s.io/apiserver/pkg/server/healthz —
failed checks do not surface in the 503 body; verbose breakdown is
operator-only.

Public `/readyz` 503 reasons are intentionally low-cardinality. Operators
read them from the structured `slog` record (the wire body carries an empty
details array):

| slog level | slog `msg` | slog `status` | slog `reason` | Meaning |
|------------|------------|---------------|---------------|---------|
| `Warn` | `readyz unhealthy` | `unhealthy` | `readiness_failed` | One or more cells/probes failed (HTTP 503), or the readiness aggregator failed closed. NOTE: an internal computation panic is recovered and logged under a **different** `msg="readyz: recovered panic during readiness computation"` (Error level, `internal_reason=readiness_computation_failed`) — filter that msg separately, it does not appear under `readyz unhealthy`. |
| `Info` | `readyz degraded` | `degraded` | — (no `reason` attr) | One or more cells/probes are degraded but serving (**HTTP 200**, fail-open). Emitted at Info so operators can observe degraded dependency `error_msg` without a Warn-level alert. |
| `Info` | `readyz: shutting down (graceful_shutdown)` | `shutting_down` | `graceful_shutdown` | The process is draining and should be removed from load balancer traffic. |

On-call dashboards / alert rules that filter by level alone will miss the
`degraded` and `shutting_down` paths; query `level=Warn AND msg="readyz
unhealthy"`, `level=Info AND msg="readyz degraded"`, and `level=Info AND
status="shutting_down"` to capture every non-healthy outcome (degraded is a
200, the other two are 503).

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
	    "adapters": { "storage": "postgres", "event_bus": "rabbitmq" }
	  }
}
```

200 (degraded — one or more cells/probes degraded but serving). A degraded
service is **fail-open**: it returns HTTP 200 so it is NOT evicted from load
balancer / kubelet rotation (ref: envoyproxy/envoy admin `/ready` — DEGRADED
returns 200). The verbose wire body carries the per-dependency `{status,
duration_ms}` exactly like the healthy case; `error_msg` is **never** on the
wire (it rides the slog channel only):

```json
{
  "data": {
	    "status": "degraded",
	    "cells":   { "accesscore": "healthy", "auditcore": "degraded" },
	    "dependencies": {
	      "postgres_ready": { "status": "healthy", "duration_ms": 3 },
	      "rabbitmq_ready":  { "status": "degraded", "duration_ms": 8 }
	    },
	    "adapters": { "storage": "postgres", "event_bus": "rabbitmq" }
	  }
}
```

The degraded breakdown is additionally emitted to slog at **Info** level
(`msg="readyz degraded"`), so operators see the degraded dependency
`error_msg` without a Warn-level alert firing. The slog shape is identical to
the unhealthy example below, only the level (`INFO`) and `msg`
(`readyz degraded`) differ, and there is no `reason` attr.

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

Verbose 503 slog example（text handler，`-log-format=text` 默认）。实际是单行
key=value（这里按字段折行只为可读）。`request_id` / `correlation_id` 由 RequestID
middleware 经 contextHandler 注入（probe 端点有；`trace_id` 默认无——见上文 preamble）。
**注意 `error_msg` 的引号规则**（text handler 的 logfmt 行为，决定下方 grep 怎么写）：含
空格或 `=` 的值加引号、空值输出 `error_msg=""`、单 token 无特殊字符（如 `error_msg=timeout`）
**不加引号**：

```
time=2026-05-26T03:50:06Z level=WARN msg="readyz unhealthy"
  request_id=7f3c… correlation_id=7f3c… status=unhealthy reason=readiness_failed
  cells=map[accesscore:healthy auditcore:degraded]
  dependencies.postgres_ready.status=healthy
  dependencies.postgres_ready.duration_ms=3
  dependencies.postgres_ready.error_msg=""
  dependencies.rabbitmq_ready.status=unhealthy
  dependencies.rabbitmq_ready.duration_ms=12
  dependencies.rabbitmq_ready.error_msg="dial failed password=<REDACTED> host=mq"
  adapters=map[storage:postgres event_bus:rabbitmq]
```

Verbose 503 slog example（JSON handler，`-log-format=json`）：

```json
{
  "level": "WARN",
  "msg": "readyz unhealthy",
  "request_id": "7f3c…",
  "correlation_id": "7f3c…",
  "status": "unhealthy",
  "reason": "readiness_failed",
  "cells": {"accesscore": "healthy", "auditcore": "degraded"},
  "dependencies": {
    "postgres_ready": {"status": "healthy", "duration_ms": 3, "error_msg": ""},
    "rabbitmq_ready": {"status": "unhealthy", "duration_ms": 12, "error_msg": "dial failed password=<REDACTED> host=mq"}
  },
  "adapters": {"storage": "postgres", "event_bus": "rabbitmq"}
}
```

The redacted `error_msg` retains the surrounding key context (`dial failed … host=mq`)
and masks only the sensitive value (`password=<REDACTED>`); it is **not** truncated
(slog has no wire-capacity constraint). A degraded 200 record is identical except
`level=INFO`, `msg="readyz degraded"`, and no `reason` attr.

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

> JSON 是推荐的诊断格式——嵌套对象路径可被 jq / LogQL 直接索引，且不受 text handler
> 的引号歧义影响。需要按请求关联时，readyz record 带 `request_id` / `correlation_id`
> （`trace_id` 在 probe 端点默认无，见 preamble）。

### JSON handler（`-log-format=json`）

```bash
# unhealthy(503) 与 degraded(200) 两类非健康记录都看（degraded 是 Info，别只过滤 unhealthy）
kubectl logs <pod> | jq 'select(.msg == "readyz unhealthy" or .msg == "readyz degraded") | {msg, request_id, dependencies}'

# 定位某个 probe 的 error_msg（healthy probe 为空字符串）
kubectl logs <pod> | jq 'select(.msg|test("readyz (unhealthy|degraded)")) | .dependencies.rabbitmq_ready.error_msg'

# 按 request_id 关联一次具体请求的所有日志（R2：readyz record 现带关联字段）
kubectl logs <pod> | jq 'select(.request_id == "7f3c…")'

# Grafana / Loki LogQL 查询示例（结构化字段索引）：
# {app="myapp"} | json | msg = "readyz unhealthy" | dependencies_rabbitmq_ready_status = "unhealthy"
```

### Text handler（`-log-format=text`，默认）

```bash
# 过滤 unhealthy(503) 与 degraded(200) 两类记录
kubectl logs <pod> | grep -E 'msg="readyz (unhealthy|degraded)"'

# 提取某个 probe 的 error_msg。值可能加引号（含空格/=，如 "dial … <REDACTED>"）也可能
# 不加引号（单 token，如 timeout）——两种都匹配，否则会漏掉 unquoted 值：
kubectl logs <pod> | grep -E 'msg="readyz (unhealthy|degraded)"' \
  | grep -oE 'dependencies\.rabbitmq_ready\.error_msg=("[^"]*"|[^ ]+)'
```

text handler 输出形态是 `dependencies.<probe_name>.<field>=<value>` 而非嵌套 `{}` 块，
Loki / Grafana 通过 key=value 解析直接索引。`error_msg` 的引号取决于值内容（含空格/`=`
→ 加引号；空 → `""`；单 token → 不加引号），所以**只匹配 `error_msg="…"` 会漏掉 unquoted
值**。另：若 error message 本身含双引号，text handler 会转义为 `\"`，上面的 `"[^"]*"`
分支会在转义引号处提前截断——这类带引号的 error 用 grep 难以稳健提取。**生产诊断推荐
JSON handler**（`-log-format=json`）：嵌套对象路径不受引号规则与转义影响，jq / LogQL 可
稳定索引。

### Waiving the verbose endpoint

For test harnesses or single-node demos that genuinely do not want the
verbose debug channel at all, set:

```
GOCELL_READYZ_VERBOSE_DISABLED=1
```

When `VerboseDisabled` is in effect, every `?verbose` request is answered
with the normal aggregate response (200 plain status body or 503 error
envelope) instead of 401. `VerboseDisabled=1` is
rejected in `GOCELL_ADAPTER_MODE=real`: production must retain the
token-gated diagnostic channel.

### Strict 401 semantics

`?verbose` requests are routed like this (the response status here is
independent of the probe outcome — a verbose-authorised 503 still uses the
same error envelope described under "Response shape"):

| Server state | Request | Response |
|--------------|---------|----------|
| `WithVerboseDisabled()` set (e.g. `GOCELL_READYZ_VERBOSE_DISABLED=1`) | any `?verbose` | **200 plain status body** (no verbose fields) / **503 error envelope** |
| token configured + header matches | `?verbose` with matching `X-Readyz-Token` | **200 verbose body** (cells/dependencies/adapters) / **503 plain error envelope** — a verbose-authorised 503 carries no verbose fields on the wire; status/reason ride on slog only (see preamble) |
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
platform Cell registers a cell-level repo readiness probe via the cellgen-generated
`<cellpkg>.RegisterReadiness(reg, prober)` helper (emitted into `healthz_gen.go`).
These probes represent a **distinct failure domain**:
they execute a representative query against the Cell's own relation(s), surfacing
schema/migration drift, missing tables, and table-level permission loss that a
connection Ping cannot detect.

| Probe name | Owning Cell | Probed relation(s) | Backend |
|---|---|---|---|
| `configcore_repo_ready` | configcore | `config_entries`, `feature_flags` | PG only; mem stores always return nil (ready) |
| `accesscore_repo_ready` | accesscore | `sessions`, `policies` | PG only; mem stores always return nil (ready). Composite — ready only when every probed relation is reachable (#1346) |
| `auditcore_repo_ready` | auditcore | `audit_entries` (via `Tail`) | PG only; mem stores always return nil (ready) |

### Adapter-level: serving-role capability probe

`postgres_app_role_restricted_ready` is an **adapter-level** probe registered by
`adapters/postgres` (not cell-level). It queries
`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user` and
fails (503) when the serving role is a superuser or carries `BYPASSRLS`. This is a
**distinct failure domain** from `postgres_ready` (connection alive) and from the
cell-level repo probes (schema shape):

- Green `postgres_ready` + failing `postgres_app_role_restricted_ready` → serving
  pool is connected but running as a privileged role that bypasses RLS. Remediation:
  change `GOCELL_CONFIGCORE_DATABASE_URL` to use role `gocell_app` and restart.
- Red on startup: local dev env still uses the admin `gocell` role; see
  `docs/ops/local-docker-deploy.md` §Dual-role PostgreSQL.

This probe exists because `schema_guard.verifyRLS` only validates that RLS
policies are **defined** on the correct tables; it does not verify that the
**serving connection role** honours those policies. A superuser or `BYPASSRLS`
role ignores all policies at runtime even if the schema is correct.

ref: `docs/architecture/202606071200-1676-adr-restricted-app-serving-pool.md` §Decision.

#### Migration 065 and the optional `gocell_audit_admin` role (#1810)

Migration 065 adds the `audit_admin_read_all` RLS policy on `audit_entries`. Its
schema-guard validation via `schema_guard.VerifyExpectedShape` applies only when
the `gocell_audit_admin` role exists in the database:

- **Role absent** (default — `GOCELL_AUDIT_ADMIN_PASSWORD` unset at initdb): migration
  064 runs as a no-op (the role does not exist, so the policy body referencing it is not
  installed). `schema_guard` expects 1 RLS policy on `audit_entries` (the existing
  `tenant_isolation` policy from migration 055). No `/readyz` impact.
- **Role present, policy installed**: `schema_guard.VerifyExpectedShape` expects 2 RLS
  policies on `audit_entries` (`tenant_isolation` + `audit_admin_read_all`). If the role
  was provisioned after migration 065 ran (i.e. the role did not exist when goose applied
  migration 065, so the policy body was skipped), the expected policy count mismatches and
  `/readyz` returns **503** via `postgres_app_role_restricted_ready` (schema drift).
  Remediation: apply the policy and grant directly — goose will not re-run an
  already-recorded migration version, so run the following SQL as the database owner
  (`gocell` role):

  ```sql
  -- Idempotent repair: (re)create the policy and grant if missing.
  -- Run as the database owner (gocell) against the target database.
  DO $$
  BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gocell_audit_admin') THEN
      IF NOT EXISTS (
        SELECT 1 FROM pg_policies
        WHERE tablename = 'audit_entries' AND policyname = 'audit_admin_read_all'
      ) THEN
        EXECUTE 'CREATE POLICY audit_admin_read_all ON audit_entries
                 FOR SELECT TO gocell_audit_admin USING (true)';
        EXECUTE 'GRANT SELECT ON audit_entries TO gocell_audit_admin';
      END IF;
    END IF;
  END $$;
  ```

  After executing this SQL, restart corebundle; `/readyz` turns green on the next cycle.

When provisioned (`GOCELL_AUDIT_ADMIN_DSN` set), the `gocell_audit_admin` admin read pool
contributes one `/readyz` probe: **`postgres_audit_admin_restricted_ready`**. It reuses the
serving pool's restricted-role check (`Pool.AppRoleRestrictedCheck`) to assert the admin
pool's `current_user` is neither a superuser nor `BYPASSRLS` — the role must read
cross-tenant via the role-scoped permissive RLS policy (migration 065), never via
`BYPASSRLS` (ADR #1676). The probe doubles as the admin pool's liveness signal (it issues
a `pg_roles` query), so a pool connectivity failure or a mis-provisioned (superuser /
BYPASSRLS) admin role turns `/readyz` red rather than only surfacing as a 5xx on a
super-admin cross-tenant request. The probe name is distinct from the serving pool's
`postgres_app_role_restricted_ready` (no collision). When the admin pool is not
provisioned, no probe is registered (the capability is absent; super-admin reads stay 501).

These probes are **not synonymous** with `postgres_ready`. A green `postgres_ready`
and a failing `accesscore_repo_ready` means the PG connection is alive but the
`sessions` **or** `policies` table is inaccessible (the probe aggregates both and
fails closed on the first not-ready relation) — a different remediation path
(migration replay, permission grant) than a connection failure. Operators must
monitor both probe families independently.

Cell-level repo probe names appear in the verbose breakdown under `dependencies`:

```json
"dependencies": {
  "postgres_ready":                       { "status": "healthy", "duration_ms": 3 },
  "postgres_app_role_restricted_ready":   { "status": "healthy", "duration_ms": 1 },
  "configcore_repo_ready":                { "status": "healthy", "duration_ms": 2 },
  "accesscore_repo_ready":                { "status": "healthy", "duration_ms": 1 },
  "auditcore_repo_ready":                 { "status": "healthy", "duration_ms": 2 }
}
```

ref: `docs/architecture/202605161030-adr-cell-repo-readyz-probe.md` §D1 — differentiated
failure domain rationale; `.claude/rules/gocell/observability.md` §Cell 级别 Repo
Readiness Probe.

## Probe contract

Every probe registered through `healthz.Aggregator.Register` is
wrapped internally with a race-pattern guard (`wrapCtxSafe`). The outer
probe is structurally guaranteed to return when the aggregate readyz
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

## Projection probes

### projection_journal_ready (deferred to #1504 PR-03)

The `projection_journal_ready` ProbeName is declared in `adapters/postgres`
(`ProbeProjectionJournalReady`, EPIC #1504 PR-01) and implemented by
`PGProjectionEventSource.RepoReady`, but is **not yet registered** with any composition
root — wiring lands in #1504 PR-03 when the durable projection source replaces the
outbox-backed reader. Until then this probe does **not** appear in `/readyz?verbose`
output. After registration it reports `projection_events` table reachability + table-level
permissions (a `SELECT 1 FROM projection_events WHERE false` representative query),
surfacing schema/migration drift that the pool-level `postgres_ready` ping cannot detect —
same semantics as the `*_repo_ready` cell probes.

## Saga probes

### saga_coordinator_ready (deferred to saga-as-cell migration)

The `saga_coordinator_ready` ProbeName is declared in `runtime/saga` (PR #1210 C7) but is
**not yet registered** with any cell — cell-side `RegisterReadiness` wiring lands after the
saga-as-cell migration tracked in #978. Until that migration ships, this probe does **not**
appear in `/readyz?verbose` output. After migration it will report journal backend availability
for the saga coordinator (same semantics as the `*_repo_ready` cell probes).

### `<cellID>_saga_tailer_<projectionID>_ready` (saga-journal projection, #1609 PR-05)

Each **saga-journal projection** (a `role: subscribe` CU with `projectionSource: saga-journal`)
registers one dependency-availability probe named `<cellID>_saga_tailer_<projectionID>_ready`
(`healthz.SagaTailerReadyProbeName`). Bootstrap's phase6 saga drain
(`drainCellSagaProjections`) wires it onto the health aggregator when it constructs the
projection's `runtime/saga/tailer.Tailer`. Unlike `saga_coordinator_ready`, this probe **is**
registered today (a deployment that declares a saga-journal projection surfaces it in
`/readyz?verbose`).

The name carries **both** `cellID` and `projectionID` because the projection identity — and its
distlock leader key (`saga-journal-tailer:<len>:<cell>:<len>:<proj>`) and checkpoint key — is the
`(cellID, projectionID)` pair (two cells may legitimately declare the same `projectionID`).

It is a **dependency-availability** probe (not a lag probe): once the Tailer is running it reports
not-ready only if EITHER the leader-gate distlock backend was unreachable on the most recent
acquire OR journal/checkpoint storage is unreachable (it exercises `replay.Head` + `store.LoadOffset`).
A contended acquire (another replica is the leader) and a non-zero replay lag are **normal** and
keep the probe healthy — lag is exposed separately as the `ObserveLag` metric gauge, not a second
probe. It is an ops contract: renaming it requires synchronizing this doc, dashboards and alerts
(`docs/ops/saga-runbook.md` 场景 5).

#### Saga instance lifecycle and readyz semantics

A saga instance progresses through the following status values (iota+1 constants in
`kernel/saga`):

<!-- gocell:generated:saga-status-table — DO NOT EDIT (regen: gocell generate saga-coverage) -->
| Status | Value | Phase | Terminal? |
|---|---|---|---|
| `Pending` | 1 | Not yet started | No |
| `Running` | 2 | Executing steps forward | No |
| `Compensating` | 3 | A step failed; rolling back in reverse | No |
| `Succeeded` | 4 | All steps committed | Yes |
| `Failed` | 5 | Forward failure, no rollback entered | Yes |
| `Compensated` | 6 | Rollback completed cleanly | Yes |
| `Expired` | 7 | Overall timeout elapsed | Yes |
| `CompensationFailed` | 8 | Rollback itself encountered a step failure | Yes |
<!-- /gocell:generated:saga-status-table -->

`saga_coordinator_ready` is a **Coordinator daemon-level probe** — it reports whether the
leader election is established and the heartbeat tick is healthy. It is **not** a per-instance
probe. The probe does not distinguish between running instance statuses (`Pending` / `Running`
/ `Compensating`); those are normal lifecycle states, not infrastructure faults.

**`Compensating` and `CompensationFailed` do not affect readyz.** Both are business-lifecycle
statuses:

- `Compensating` is a transient non-terminal state; the Coordinator drives it to a terminal
  status (`Compensated`, `CompensationFailed`, or `Expired`).
- `CompensationFailed` (introduced by PR #1210, status=8) is a terminal state indicating the
  rollback phase encountered at least one `CompensateFunc` error. It is structurally distinct
  from `Failed` (forward failure, no rollback) — operators can distinguish the two root causes
  by reading the terminal status alone. This is a business-level outcome, not a Coordinator
  daemon fault.

Ops diagnostics for `CompensationFailed` instances use the `saga_events` table
(see `docs/ops/saga-runbook.md` §场景 2), not readyz.

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
