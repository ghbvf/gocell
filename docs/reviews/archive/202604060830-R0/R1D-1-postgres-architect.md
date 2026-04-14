# R1D-1 Architect Review: adapters/postgres

**Reviewer**: Architect Agent  
**Scope**: `adapters/postgres/` (~1200 LOC, 9 source files + 7 test files)  
**Date**: 2026-04-06  
**Verdict**: 3 P0, 4 P1, 3 P2

---

## Files Reviewed

| File | LOC (approx) | Role |
|---|---|---|
| pool.go | 160 | pgxpool wrapper, Config, Health |
| tx_manager.go | 152 | RunInTx with savepoint nesting + panic recovery |
| migrator.go | 362 | embed.FS SQL migrations with up/down/status |
| outbox_writer.go | 69 | outbox.Writer implementation (tx-context pattern) |
| outbox_relay.go | 263 | outbox.Relay + worker.Worker (poll/publish/cleanup) |
| errors.go | 29 | ERR_ADAPTER_PG_* error codes |
| helpers.go | 87 | RowScanner + QueryBuilder |
| embed.go | 26 | Embedded migration FS |
| doc.go | 17 | Package doc |

---

## Findings Table

| # | Severity | Dimension | Finding | File:Line | Impact |
|---|----------|-----------|---------|-----------|--------|
| F1 | **P0** | Dependency Direction | `outbox_relay.go` imports `runtime/worker` -- adapters/ must only implement interfaces defined in kernel/ or runtime/, but must NOT import runtime/ types to implement at the same time as kernel/ types. The real issue: `OutboxRelay` satisfies two interface contracts from two different layers (`outbox.Relay` from kernel/ and `worker.Worker` from runtime/) creating a bidirectional coupling anchor. | outbox_relay.go:12,19-20 | **High** |
| F2 | **P0** | Interface Stability / Data Loss | `Entry.Topic` field (added to `kernel/outbox/outbox.go:21`) is never persisted by `OutboxWriter.Write()` and never read back by `OutboxRelay.pollOnce()`. The DB schema `001_create_outbox_entries.up.sql` has no `topic` column. If a caller sets `entry.Topic = "orders.v2"`, it is silently dropped on write, and `RoutingTopic()` in the relay always falls back to `EventType`. This is a silent data-loss bug for any L2 cell using explicit topic routing. | outbox_writer.go:49-51, outbox_relay.go:146-148, migrations/001_create_outbox_entries.up.sql | **High** |
| F3 | **P0** | Migrator Design | `Migrator` doc (line 41) claims "advisory locking to prevent concurrent execution (adopted from Watermill's approach)" but **no advisory lock** (`pg_advisory_lock` / `pg_try_advisory_lock`) exists in the code. Concurrent migration runs (e.g., rolling deploy) will race on `schema_migrations` reads/writes, potentially double-applying migrations or corrupting state. | migrator.go:41 (doc), migrator.go:81-105 (Up method) | **High** |
| F4 | **P1** | Migrator Design | `latestApplied()` detects no-rows via string comparison `err.Error() == "no rows in result set"` instead of `errors.Is(err, pgx.ErrNoRows)`. This is fragile -- if pgx changes the error message text in a minor version, the code breaks silently. | migrator.go:275 | **Medium** |
| F5 | **P1** | Error Code Duplication | `ErrAdapterPGNoTx` in `errors.go:21` has value `"ERR_ADAPTER_NO_TX"`, and `errcode.ErrAdapterNoTx` in `pkg/errcode/errcode.go:36` has the identical value `"ERR_ADAPTER_NO_TX"`. Two constants in different packages resolve to the same string. This will cause confusion when matching error codes across layers -- callers importing `errcode.ErrAdapterNoTx` and callers importing `postgres.ErrAdapterPGNoTx` are testing for the same string but via different symbols. | errors.go:21, pkg/errcode/errcode.go:36 | **Medium** |
| F6 | **P1** | SQL Injection Surface | `Migrator.ensureTable()`, `appliedVersions()`, `appliedDetails()`, `latestApplied()`, `applyMigration()`, `rollbackMigration()` all use `fmt.Sprintf` to interpolate `m.tableName` directly into SQL strings. While the table name comes from constructor input (not user input), this establishes a pattern that could be exploited if the API surface is ever widened. Should either validate the table name against `[a-z_]+` regex or use pgx identifier quoting. | migrator.go:68,224,247,271,306,345 | **Medium** |
| F7 | **P1** | Transaction Boundary / Relay Correctness | In `pollOnce()`, after publishing an entry, if `r.pub.Publish()` succeeds but the subsequent `tx.Exec(markQuery)` fails, the entry is published to the broker but NOT marked as published in the DB. On the next poll cycle, the same entry will be fetched and re-published, causing duplicate delivery. Additionally, if one entry in the batch fails to publish (line 202: `continue`), the successfully-published entries' marks are committed in the same transaction -- but the skipped entry remains unpublished. The partial-batch-commit-on-success-skip-on-failure within a single transaction is correct for at-least-once, but the Exec failure case (line 207-211) silently swallows the error with only a log. | outbox_relay.go:186-218 | **Medium** |
| F8 | **P2** | Pool Configuration | `MinConns` is not configurable. pgxpool defaults to 0 min connections, meaning under bursty load the pool repeatedly tears down and rebuilds connections. For production use, `MinConns` should be exposed (env: `GOCELL_PG_MIN_CONNS`, default: 2-4). | pool.go:16-21,107-109 | **Low** |
| F9 | **P2** | Consistency Level | The `OutboxRelay` cleanup deletes by `created_at` (`WHERE published = true AND created_at < $1`) but should arguably delete by `published_at` (when it was actually published, not when the entry was created). An entry created 4 days ago but published 1 day ago would be prematurely deleted with the current logic if `RetentionPeriod` is 72h. | outbox_relay.go:251 | **Low** |
| F10 | **P2** | Observability | `ErrAdapterPGTxTimeout` (errors.go:15) is declared but never used anywhere in the codebase. Dead code. | errors.go:15 | **Low** |

---

## Deep Analysis: F1 -- Layering Violation in outbox_relay.go

### Current State

```go
// outbox_relay.go
import (
    "github.com/ghbvf/gocell/kernel/outbox"     // OK: adapter implements kernel interface
    "github.com/ghbvf/gocell/runtime/worker"     // VIOLATION: adapter imports runtime
)

var (
    _ outbox.Relay  = (*OutboxRelay)(nil)  // kernel interface
    _ worker.Worker = (*OutboxRelay)(nil)  // runtime interface
)
```

### Why This is a Problem

The GoCell dependency rule states:

> adapters/ implements kernel/ or runtime/ defined interfaces

Implementing a kernel interface (outbox.Relay) is fine. Implementing a runtime interface (worker.Worker) is also fine in isolation. But **importing** runtime/worker to declare the compile-time check and structurally couple the adapter to both layers creates a dependency: `adapters/postgres -> runtime/worker` AND `runtime/bootstrap -> runtime/worker`. If runtime/worker ever changes its Worker interface, the adapter breaks -- this is the wrong direction for an adapter to depend.

The deeper issue is that `outbox.Relay` and `worker.Worker` have **identical method signatures** (`Start(ctx) error`, `Stop(ctx) error`). This is by design -- the relay IS a worker. The question is where to codify that relationship.

### Recommended Fix Options

**Option A (Preferred): Remove the worker.Worker compile-time check from the adapter.**

The structural match is already guaranteed because `outbox.Relay` has the same `Start`/`Stop` signature as `worker.Worker`. The bootstrap code (which lives in runtime/ and CAN import both) should do the type assertion:

```go
// runtime/bootstrap/bootstrap.go (already imports both packages)
relay := postgresAdapter.NewOutboxRelay(...)
// relay satisfies outbox.Relay, which structurally matches worker.Worker
wg.Add(relay) // WorkerGroup.Add(w Worker) -- duck typing works
```

Change in outbox_relay.go:
- Remove `"github.com/ghbvf/gocell/runtime/worker"` import
- Remove `_ worker.Worker = (*OutboxRelay)(nil)` assertion
- Keep `_ outbox.Relay = (*OutboxRelay)(nil)` assertion

This is safe because Go uses structural typing -- any type with `Start(context.Context) error` and `Stop(context.Context) error` satisfies `worker.Worker` without importing it.

**Option B: Promote Worker interface to kernel.**

Move the `Worker` interface definition to `kernel/cell/interfaces.go` (e.g., as `BackgroundTask`). Then adapters import only kernel. However, this pollutes kernel with runtime concerns and is not recommended.

**Option C: Introduce a shared interfaces package.**

Create `pkg/lifecycle` with `Startable` / `Stoppable` interfaces. Both kernel/outbox and runtime/worker reference it. Over-engineered for two methods.

### Verdict

**Option A is the correct fix.** It requires removing 2 lines from outbox_relay.go and zero changes elsewhere, because Go's structural typing already makes `OutboxRelay` compatible with `worker.Worker`.

---

## Deep Analysis: F2 -- Topic Field Not Persisted

### Current State

`kernel/outbox.Entry` declares:
```go
Topic string // broker routing key; falls back to EventType if empty
```

`OutboxWriter.Write()` INSERT query:
```sql
INSERT INTO outbox_entries
    (id, aggregate_id, aggregate_type, event_type, payload, metadata, created_at, published)
    VALUES ($1, $2, $3, $4, $5, $6, $7, false)
```

No `topic` column in `outbox_entries` table. No `topic` in the SELECT query of `OutboxRelay.pollOnce()`.

### Impact

Any caller that sets `entry.Topic` to route to a specific broker topic (e.g., `"orders.v2"` instead of the default `"order.created"`) will have that routing information silently dropped. The relay will publish to `EventType` instead. This breaks the `RoutingTopic()` contract for any non-trivial topic routing scenario.

### Fix

1. Add migration `002_add_topic_column.up.sql`:
   ```sql
   ALTER TABLE outbox_entries ADD COLUMN topic TEXT NOT NULL DEFAULT '';
   ```
2. Add `002_add_topic_column.down.sql`:
   ```sql
   ALTER TABLE outbox_entries DROP COLUMN IF EXISTS topic;
   ```
3. Update `OutboxWriter.Write()` to include `topic` in the INSERT.
4. Update `OutboxRelay.pollOnce()` SELECT to include `topic` and scan it into `e.Topic`.

---

## Deep Analysis: F3 -- Missing Advisory Lock in Migrator

### Claimed vs. Actual

The `Migrator` struct doc says:
> "using advisory locking to prevent concurrent execution (adopted from Watermill's approach)"

But no `pg_advisory_lock`, `pg_try_advisory_lock`, or any locking mechanism exists in the code. The `Up()` method:

1. Reads applied versions (non-transactional SELECT)
2. For each unapplied migration, calls `applyMigration()` which starts its own transaction

Two concurrent `Up()` calls can both read the same set of applied versions, then both attempt to apply the same migration. The second will fail on the `INSERT INTO schema_migrations` primary key constraint, but by then the DDL in the migration SQL has already been executed twice, which can cause errors (or worse, succeed silently for non-idempotent DDL).

### Fix

Wrap the entire `Up()` method in an advisory lock:

```go
func (m *Migrator) Up(ctx context.Context) error {
    // Acquire advisory lock to prevent concurrent migrations.
    const lockID = 1234567890 // arbitrary but stable
    if _, err := m.pool.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
        return errcode.Wrap(ErrAdapterPGMigrate, "postgres: acquire migration lock", err)
    }
    defer m.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
    
    // ... existing logic
}
```

Same for `Down()`.

---

## Architectural Summary

### Positive Observations

1. **Interface compliance is explicit**: compile-time `var _ outbox.Writer = (*OutboxWriter)(nil)` checks are present and correct.
2. **Transaction embedding via context** is clean -- `CtxWithTx` / `TxFromContext` pattern is well-documented and tested with savepoint nesting.
3. **Panic safety** is handled correctly in both `RunInTx` and `runInSavepoint`.
4. **Test coverage** is strong for unit tests. The integration test file covers Pool, TxManager, Migrator, and OutboxWriter end-to-end with testcontainers.
5. **Error wrapping** consistently uses `pkg/errcode` with context messages.
6. **Migration file naming** and `embed.FS` integration are clean and testable.

### Dependency Direction Summary

```
adapters/postgres imports:
  + kernel/outbox          -- CORRECT (implements kernel interfaces)
  + pkg/errcode            -- CORRECT (shared utility)
  - runtime/worker         -- VIOLATION (F1)
  + github.com/jackc/pgx   -- CORRECT (external driver)
```

### Consistency Level Assessment

The outbox pattern (Writer + Relay + Publisher) correctly supports **L2 OutboxFact**: local transaction + outbox publish. The `OutboxWriter` enforces tx-context atomicity. The `OutboxRelay` implements poll-publish-mark with `FOR UPDATE SKIP LOCKED` for safe concurrent consumption. The Topic field gap (F2) is the main correctness risk for L2 workflows.

---

## Action Items by Priority

| Priority | Finding | Action | Owner |
|----------|---------|--------|-------|
| P0 | F1 | Remove `runtime/worker` import and compile-time check from outbox_relay.go | adapter maintainer |
| P0 | F2 | Add `topic` column migration + update Write/Relay queries | adapter maintainer |
| P0 | F3 | Implement `pg_advisory_lock` in Migrator.Up() and Migrator.Down() | adapter maintainer |
| P1 | F4 | Replace string comparison with `errors.Is(err, pgx.ErrNoRows)` | adapter maintainer |
| P1 | F5 | Remove duplicate `ErrAdapterNoTx` from `pkg/errcode` or unify | kernel + adapter |
| P1 | F6 | Validate `tableName` with regex or use identifier quoting | adapter maintainer |
| P1 | F7 | Document at-least-once semantics; consider per-entry tx for mark | adapter maintainer |
| P2 | F8 | Add MinConns to Config | adapter maintainer |
| P2 | F9 | Change cleanup WHERE to use `published_at` instead of `created_at` | adapter maintainer |
| P2 | F10 | Remove unused `ErrAdapterPGTxTimeout` | adapter maintainer |
