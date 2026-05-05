# ADR: Refresh Store — Ambient-Only TX + Idle/Grace Extension

**Date**: 2026-05-05  
**Status**: Accepted  
**Implements**: B2-A-08, B2-A-09, B2-A-10, B2-A-11, B2-A-13, X12, X14  
**PR**: PR-V1-PG-REFRESH-HARDEN-AND-IDLE-GRACE

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
- `postgres_indexes_valid_ready`: calls `InvalidIndexCheck(ctx, pool)` which wraps
  `DetectInvalidIndexes` and returns a non-nil error when any `indisvalid=false`
  row exists

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

### B2-A-13 — RedactError in tx_manager rollback slog

All four `slog.Error` calls in `adapters/postgres/tx_manager.go` for rollback
failures now wrap the error string with `pkg/redaction.RedactError(err).Error()`
before logging, ensuring DSN/token strings in rollback errors do not leak to logs.

### X12 — MaxIdle sliding-window idle-expiry (REFRESH-IDLE-EXPIRE)

Migration 016 adds `idle_expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+30d`.

- `Issue`: sets `idle_expires_at = now + Policy.MaxIdle`
- `Rotate`: child row's `idle_expires_at = now + Policy.MaxIdle` (sliding window
  reset on every successful rotation)
- `validateRow`: rejects with `idle_expired` when `idle_expires_at ≤ now` and
  `Policy.MaxIdle > 0`
- `gcBatchSQL`: uses `LEAST(expires_at, idle_expires_at) < $1` so idle-expired
  rows are GC'd even when their absolute `expires_at` is still in the future
- Pre-016 rows have the column defaulted to `created_at + 30d`; stores that have
  not applied the migration set `MaxIdle = 0` to disable the idle check

### X14 — GraceMaxReuses grace counter cap (REFRESH-GRACE-COUNTER)

Migration 016 adds `first_used_at TIMESTAMPTZ NULL` and `used_times INT NOT NULL DEFAULT 0`.

- `Rotate` / `validatePresentedLocked`: when the presented token is within the
  ReuseInterval grace window AND `Policy.GraceMaxReuses > 0` AND
  `used_times >= GraceMaxReuses` → cascade revoke + ErrRejected (same path as
  out-of-window reuse detection)
- On each in-grace re-present, `markGraceUsedSQL` increments `used_times` and sets
  `first_used_at = COALESCE(first_used_at, now)` so the first grace re-use time is
  preserved for auditing
- Zero `GraceMaxReuses` (default for pre-016 stores) disables the counter cap

**Default values** (in `runtime/auth/refresh/types.go`):
- `DefaultMaxIdle = 30 * 24 * time.Hour` (30 days, matching Zitadel session TTL)
- `DefaultGraceMaxReuses = 3` (tolerates SPA double-submit + network retry)

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
4. **Dual readyz**: invalid indexes (CONCURRENTLY-failed) now surface in the health
   endpoint before they cause query failures.
5. **Redacted rollback logs**: DSN strings from Postgres rollback errors cannot
   leak to log sinks.

### Trade-offs

- The grace counter means T10 (100 concurrent Rotate) uses `GraceMaxReuses=200` in
  the storetest contract so the concurrent-CAS property is preserved for this test.
  Production deployments with `GraceMaxReuses=3` cap concurrent grace retries to 3.
- Migration 016 is additive (ALTER TABLE ADD COLUMN IF NOT EXISTS + nullable/default
  columns). No data migration needed; old rows get the 30-day idle default.
- Stores that have not applied migration 016 must set `Policy.MaxIdle=0` and
  `Policy.GraceMaxReuses=0` to disable the new checks at the application layer.

---

## References

- ory/hydra `persistence/sql/persister_oauth2.go` — reuse-detection + grace window
- zitadel `internal/api/oidc/token_refresh.go` — idle TTL per-request reset
- ory/fosite `handler/oauth2/refresh.go` — COALESCE grace guard
- `docs/architecture/202605051600-adr-pg-outbox-fencing.md` — related fencing pattern
