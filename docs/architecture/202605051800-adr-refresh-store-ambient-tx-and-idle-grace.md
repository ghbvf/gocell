# ADR: Refresh Store — Ambient-Only TX + Idle/Grace Extension

**Date**: 2026-05-05 (revised 2026-05-06 by PR#528 follow-up — finding 1+2)
**Status**: Accepted  
**Implements**: B2-A-08, B2-A-09, B2-A-10, B2-A-11, B2-A-13, X12, X14, X15
**PR**: PR-V1-PG-REFRESH-HARDEN-AND-IDLE-GRACE → PR#528 follow-up

---

## Context

The `PGRefreshStore` (adapters/postgres) originally acquired its own `pgxpool.Begin`
transactions inside `Peek` and `Rotate`. This made it impossible for callers (e.g.
`sessionlogin`) to wrap refresh operations in a shared transaction boundary without
double-nesting transactions in an unsupported way.

Additionally, the store lacked:
- An idle-expiry window (a token can expire even if MaxAge has not passed, simply
  because it has not been used)
- A grace-reuse counter cap (concurrent SPA clients can grace-retry many times
  within the ReuseInterval; without a cap a stolen token can be endlessly rotated)
- Uniform reject-path logging (reuse_detected emitted slog.Error differently from
  other reject branches, creating a subtle timing oracle)
- Dual readyz probes (only ping, no schema validity check)

---

## Decision

### B2-A-08 — Ambient-only TX model

`PGRefreshStore` no longer acquires its own transactions. It accepts an injected
`persistence.TxRunner` and delegates all multi-statement operations (Peek, Rotate)
to `txRunner.RunInTx(ctx, ...)`. When the caller already holds an ambient
transaction, `TxManager.RunInTx` creates a savepoint instead of a new top-level
transaction, making refresh operations nesting-safe.

Constructor signature changed to:
```go
func NewRefreshStore(pool, txRunner, policy, clock, randReader) (*PGRefreshStore, error)
```
`MustNewRefreshStore` is deleted. All callers (production + test) use the error-first
constructor. Cell-level callers (cell_init.go) propagate the error through `Init`.

**Archtest**: `REFRESH-AMBIENT-TX-01` (AST scan for `.Begin()` in refresh_store.go).

### Reuse cascade revoke bypasses the ambient tx

Reuse detection (`rotated_at + reuseInterval < now`) and grace exhaustion
(`used_times >= GraceMaxReuses`) trigger a cascade revoke that wipes every row
in the offending session. This SQL **must not** participate in the caller's
ambient transaction: it is a security response to a confirmed attack, and a
business-level rollback by the caller (e.g. an unrelated handler error after
the refresh check) would undo the revocation and leave the stolen chain live.

Implementation: `handleRotatedRow` runs the cascade `revokeSessionSQL`
through `s.pool.Exec(cascadeCtx, ...)` directly, bypassing `execCtx`. The
cascadeCtx is a **detached, bounded** context (see X15 below). The revoke
commits on its own connection regardless of the surrounding `RunInTx`
outcome. The reject error itself is still captured via the outer `rejectErr`
variable so that `RunInTx` commits cleanly (timing oracle defense intact).

**Test**: `TestPGRefreshStore_ReuseCascadeSurvivesAmbientRollback` —
caller `RunInTx(ctx, fn)` where `fn` triggers reuse Peek then returns an
error; after the outer rollback, a subsequent `Rotate(child)` must still
return `ErrRejected`.

### Session revoke API split

The `refresh.Store` interface now names the transaction semantics explicitly:

- `RevokeSession(ctx, sessionID)` is the business revoke path. It joins the
  ambient transaction when one exists and is used by logout flows so session
  state, refresh-chain revoke, and outbox writes commit or roll back together.
- `RevokeSessionDetached(ctx, sessionID)` is the security/compensation path.
  Durable implementations bypass the ambient transaction and detach from
  caller cancellation with `ctxutil.WithDetachedTimeout(...,
  refresh.CascadeRevokeTimeout)`.

The split is intentionally **session-only**. `RevokeUser` remains unsplit
because user-lock, user-delete, change-password, and logout-all are business
state transitions; refresh-chain revocation must stay atomic with user/session
mutations and related outbox writes. Adding `RevokeUserDetached` would make it
too easy for a caller to turn a business operation into a partially committed
cross-store side effect.

**Tests**:
- `storetest T22_RevokeSessionDetached_CascadeIgnoresCallerCancel`
- `TestPGRefreshStore_RevokeSessionDetachedSurvivesAmbientRollback`
- `sessionrefresh` and `sessionlogin` spy tests assert cascade/cleanup callers
  use `RevokeSessionDetached`, while logout and identity management keep using
  `RevokeSession` / `RevokeUser`.

### X15 — Cascade revoke detaches from caller cancellation

PR#388 closed the ambient-tx boundary but left cascade revoke bound to the
caller's request `context.Context`. If a client disconnects (HTTP/2 RST,
client timeout) **after** reuse_detected but **before** the cascade SQL
commits, the cascade write fails with `context canceled` and the offending
refresh chain stays live — re-creating the very vulnerability the bypass was
meant to close, just shifted from "ambient rollback" to "request lifecycle".

PR#528 fixes this by wrapping the cascade pool.Exec call in a detached,
bounded context:

```go
cascadeCtx, cancelCascade := ctxutil.WithDetachedTimeout(ctx, refresh.CascadeRevokeTimeout)
defer cancelCascade()
s.pool.Exec(cascadeCtx, revokeSessionSQL, ...)
```

`pkg/ctxutil.WithDetachedTimeout` composes `context.WithoutCancel` (Go 1.21+,
proposal #40221) with `context.WithTimeout`: the returned context inherits
all Values from the parent (trace IDs, auth principal, request id) but is
**not** canceled when parent is canceled, and carries its own absolute
deadline (`refresh.CascadeRevokeTimeout = 5 * time.Second`). The service layer
routes non-store cascade paths (subject-mismatch, session-not-found,
revoked-session, rotated-subject-mismatch, session-update-not-found) through
`Store.RevokeSessionDetached` so cascade revoke sites share the store-owned
detach + timeout policy.

This pattern is the per-call analogue of HashiCorp Vault's process-level
`core.activeContext` / `quitContext` (used by `vault/token_store.go`
`revokeInternal`). Vault detaches token revocation from request lifetime;
GoCell adopts that boundary for explicit detached revoke calls using
`WithoutCancel + WithTimeout` instead of a manager-owned process-level context
(simpler; no global lifecycle to plumb).

**Tests**:
- `pkg/ctxutil/detach_test.go` — helper boundary (parent cancel does not
  propagate; timeout fires; values preserved; cancel func releases)
- `runtime/auth/refresh/storetest.RunContractSuite::T22_RevokeSessionDetached_CascadeIgnoresCallerCancel` —
  store-level contract for calling `RevokeSessionDetached` with an already
  canceled caller ctx
- `cells/accesscore/slices/sessionrefresh/service_test.go::TestService_CascadeRevoke_UsesDetachedStoreMethod` —
  service-level cascade routing with a spy store; it asserts cascade paths call
  `RevokeSessionDetached` rather than ambient `RevokeSession`
- Black-box refresh-entry "caller cancel mid-cascade" tests are intentionally
  not provided: the literal shape (Rotate with already-canceled ctx) cannot
  reach cascade SQL because `RunInTx` fails at `pool.Begin(ctx)` first.
  Inserting cancel mid-call requires either a mocked pgxpool or
  non-deterministic timing. Coverage is layered through the helper test, the
  service-level routing test, the store contract, and
  `TestPGRefreshStore_ReuseCascadeSurvivesAmbientRollback`.

ref: golang/go context.WithoutCancel proposal#40221
ref: hashicorp/vault vault/token_store.go (quitContext detached pattern)

### Accepted timing leak: malformed wire format

`Peek` and `Rotate` call `refresh.ParseOpaque` before entering `txRunner.RunInTx`.
When `ParseOpaque` fails (invalid base64url or missing dot separator), the method
returns `rejectWithReason("malformed", "")` immediately, without a DB round-trip.
This means the malformed-wire reject path is measurably faster than reject paths that
go through the database (selector_miss, expired, revoked, reuse_detected).

**Accepted**: the information leaked is only "is this wire syntactically valid base64url
with a dot at position 22?" The base64url encoding format is defined by RFC 4648 §5
and is public knowledge; it carries no confidential information about the server's
token inventory. An adversary who measures the timing difference learns only whether
their input conforms to an openly-specified encoding — not whether any selector exists
in the database.

Eliminating this timing difference would require performing a dummy DB round-trip on
malformed input, which adds latency and resource consumption to a trivially rejected
attack vector. The trade-off is accepted.

### B2-A-09 — Uniform reject-path logging

All reject branches in `validateRow` call `rejectWithReason(reason, sessionID)`.
`reuse_detected` additionally emits `slog.Error` BEFORE the cascade SQL (a security
event requires Error level) but the function-call structure is identical across all
reject paths. The cascade SQL write is the only timing oracle that cannot be
eliminated; all other execution steps (slog formatting, error construction) are
uniform.

### B2-A-10 — Dual readyz probes

`PGResource.Checkers()` returns two named probes:
- `postgres_ready`: existing pool ping (no change)
- `postgres_indexes_valid_ready`: calls `InvalidIndexCheck(ctx, pool)`, which
  calls `DetectInvalidIndexes` and returns a non-nil error when any `indisvalid=false`
  row exists

**Update (PR#528 follow-up)**: `InvalidIndexCheck` returns a normal
`KindInternal` errcode error when invalid indexes are present. It deliberately
does **not** wrap `cell.ErrDegraded`: `/readyz` is the binary traffic gate, so
schema faults must classify as **unhealthy** (HTTP 503) rather than fail-open
**degraded** (HTTP 200). Operators still see the invalid-index list in
`/readyz?verbose` diagnostics and logs, then DROP the index manually. The
underlying query error path (connection failure, SQL error inside
`DetectInvalidIndexes`) also maps to **unhealthy**.

**Archtest**: `REFRESH-INVALID-INDEX-SINGLE-SOURCE-01` (AST scan for
`DetectInvalidIndexes` FuncDecl — asserts exactly one definition).

### B2-A-11 — Delete all MustNew from adapters/postgres and memstore

`MustNewRefreshStore` (adapters/postgres) and `memstore.MustNew` are deleted.
`memstore.New` returns `(refresh.Store, error)`. All callers converted:
- Production callers: `Init()` propagates error
- Test callers without `*testing.T`: `panic("test setup: " + err.Error())`
- Test callers with `*testing.T`: `require.NoError(t, err)`

**Archtest**: `PG-CONSTRUCTOR-MUST-FREE-01` (AST scan for exported `MustNew*`
FuncDecl in adapters/postgres non-test files).

### B2-A-13 — RedactError in tx_manager rollback slog (broadened by PR#528)

All four `slog.Error` calls in `adapters/postgres/tx_manager.go` for rollback
failures wrap the error string with `pkg/redaction.RedactError(err).Error()`
before logging.

PR#528 broadens the same fail-closed treatment to **panic payloads**:
`slog.Any("panic", r)` was originally raw `r` (an `any` returned from
`recover()`) which can carry DSN/token-shaped strings if a panic bubbled out
of a connection establishment or token-mint path. PR#528 introduces
`pkg/redaction.RedactAny(v any) any` (`nil → nil`, `error → RedactError`,
`string → RedactString`, other `→ RedactString(fmt.Sprint(v))`) and rewrites
all 11 `slog.Any("panic", X)` sites in the repo to wrap X. An archtest
`TestPanicLogMustUseRedactAny` (AST scan in `tools/archtest`) prevents
regression: any future `slog.Any("panic", X)` where X is not
`redaction.RedactAny(...)` fails CI.

Sites: `adapters/postgres/tx_manager.go` ×2 (top-level recover + savepoint
recover), `kernel/outbox/outbox.go`, `runtime/config/watcher.go`,
`runtime/http/health/health.go`, `runtime/http/health/wrap.go` ×2,
`runtime/http/middleware/recovery.go`, `runtime/http/middleware/safe_observe.go`,
`runtime/outbox/metrics_safe.go`, `runtime/worker/periodic.go`.

### X12 — MaxIdle sliding-window idle-expiry (REFRESH-IDLE-EXPIRE) — revised by PR#528

Migration 016 adds `idle_expires_at TIMESTAMPTZ NOT NULL`. PR#388's original
SQL also wrote `DEFAULT now() + INTERVAL '30 days'` and the prose claimed
pre-016 rows would receive a `created_at + 30d` semantics — neither was true.
The migration default was a metadata-only `now()+30d` (migration-time reset),
not a historical backfill. PR#528 collapses the model:

- The DDL DEFAULT is **removed**. `idle_expires_at` is `NOT NULL` with no
  default; every row must be written explicitly by Issue or Rotate.
- Migration 016 now includes a preflight `DO` block that refuses to add the
  no-default `idle_expires_at` column to a non-empty `refresh_tokens` table.
  The project is undeployed, so the accepted migration shape is "empty table
  only" rather than a backfill branch for historical refresh rows.
- `Issue`: sets `idle_expires_at = now + Policy.MaxIdle`
- `Rotate`: child row's `idle_expires_at = now + Policy.MaxIdle` (sliding
  window reset on every successful rotation)
- `validateRow`: rejects with `idle_expired` when `idle_expires_at ≤ now`
  (no zero-`MaxIdle` branch — Policy.Validate now rejects zero `MaxIdle`)
- `gcBatchSQL`: uses `LEAST(expires_at, idle_expires_at) < $1` so idle-expired
  rows are GC'd even when their absolute `expires_at` is still in the future

The migration is modified in place (project undeployed; goose
`goose_db_version` records `version_id`+`tstamp`, not file hash; CI is
ephemeral; dev `goose reset` is acceptable). No new 017 migration is created
to avoid carrying meaningless schema history as a permanent artifact.

Limitation: environments that already applied the old PR#388 version of 016
will not automatically receive the in-place DDL change because goose will
consider version 016 applied. Those environments must run `goose down -count 1`
then `goose up`, reset the dev database, or carry an explicit local follow-up
migration. That is accepted because GoCell is not deployed to production yet;
there are no external schemas that need a forward-only compatibility path.

### X14 — GraceMaxReuses grace counter cap (REFRESH-GRACE-COUNTER) — revised by PR#528

Migration 016 adds `first_used_at TIMESTAMPTZ NULL` and `used_times INT NOT NULL DEFAULT 0`
(unchanged by PR#528 — these are data columns; `used_times DEFAULT 0` is a
data initial value, not an "off switch").

- `Rotate` / `validatePresentedLocked`: when the presented token is within the
  ReuseInterval grace window AND `used_times >= Policy.GraceMaxReuses` →
  cascade revoke + ErrRejected (same path as out-of-window reuse detection).
  No zero-`GraceMaxReuses` branch — Policy.Validate rejects zero.
- On each in-grace re-present, `markGraceUsedSQL` increments `used_times` and sets
  `first_used_at = COALESCE(first_used_at, now)` so the first grace re-use time is
  preserved for auditing.

### Policy zero-value semantics (PR#528 tightening)

PR#388's `Policy.Validate` accepted `MaxIdle == 0` and `GraceMaxReuses == 0`
as "feature disabled" so pre-016 stores could opt out. With the migration
collapsed and the project unconstrained by external consumers, PR#528
collapses the dual semantics:

- `Validate` now requires `MaxIdle > 0` and `GraceMaxReuses > 0` (along with
  the existing `MaxAge > 0` and `ReuseInterval >= 0` rules). Zero is invalid.
- `runtime/auth/refresh/types.go`'s `idleFarFuture` 10-year sentinel is removed.
  `idleDeadline()` is unconditionally `now + Policy.MaxIdle`.
- `refresh_store.go` `if MaxIdle > 0 &&` / `if GraceMaxReuses > 0 &&` guards
  are removed. The store always enforces both.
- All `refresh.Policy{}` construction sites in production and tests are
  updated to provide `MaxIdle: refresh.DefaultMaxIdle` and
  `GraceMaxReuses: refresh.DefaultGraceMaxReuses` (or per-test values when
  the test asserts the corresponding behavior).

**Default values** (in `runtime/auth/refresh/types.go`):
- `DefaultMaxIdle = 30 * 24 * time.Hour` (30 days, matching Zitadel session TTL)
- `DefaultGraceMaxReuses = 3` (tolerates SPA double-submit + network retry)
- `CascadeRevokeTimeout = 5 * time.Second` (per-call detached deadline for
  cascade revoke; see X15)

These constants are exported so callers can use them as named values; they
are **not** implicit defaults applied by `Validate`.

---

## Consequences

### Positive

1. **Nesting safety**: session login can now wrap token issuance + session write in
   one transaction. Rollback reverts both, eliminating the "orphan refresh token
   after session insert fails" race.
2. **Idle security**: tokens that go unused for 30 days are rejected even if their
   absolute MaxAge has not passed. Mitigates stolen-token scenarios where an
   attacker defers use.
3. **Grace counter cap**: prevents indefinite token reuse by concurrent or malicious
   clients within the grace window. After 3 re-uses the chain is cascade-revoked.
4. **Dual readyz**: invalid indexes (CONCURRENTLY-failed) now surface as a
   readiness blocker before they cause query failures.
5. **Redacted rollback logs**: DSN strings from Postgres rollback errors cannot
   leak to log sinks.

### Trade-offs

- The grace counter means T10 (100 concurrent Rotate) uses `GraceMaxReuses=200` in
  the storetest contract so the concurrent-CAS property is preserved for this test.
  Production deployments with `GraceMaxReuses=3` cap concurrent grace retries to 3.
- PR#528 modifies migration 016 in place (drops the `idle_expires_at`
  `DEFAULT`). The project is undeployed and goose stores `version_id`+`tstamp`,
  not file hash; CI is ephemeral; dev environments need `goose reset` once.
  This is the simpler alternative to a one-line 017 "DROP DEFAULT" migration
  that would forever carry the original mismatch in schema history.
- Cascade revoke takes a hard 5-second timeout (`CascadeRevokeTimeout`). If
  PG cascade SQL exceeds 5s under load, the cascade fails with
  `context.DeadlineExceeded`; the revoke failure is logged and surfaced to the
  caller as unavailable. Neither the store nor the service performs an
  automatic retry.

---

## References

- golang/go `context.WithoutCancel` proposal #40221 — detach helper precedent
- hashicorp/vault `vault/token_store.go` `revokeInternal` — process-level
  detached `quitContext` pattern
- ory/hydra `persistence/sql/persister_oauth2.go` — reuse-detection + grace window
- zitadel `internal/api/oidc/token_refresh.go` — idle TTL per-request reset
- ory/fosite `handler/oauth2/refresh.go` — COALESCE grace guard
- `docs/architecture/202605051600-adr-pg-outbox-fencing.md` — related fencing pattern
