# R1D-1: adapters/postgres Security Review

| Field | Value |
|---|---|
| Reviewer Seat | S2 (Security / Permission) |
| Scope | `adapters/postgres/` (~1200 LOC) + `cells/*/internal/adapters/postgres/` |
| Review Basis Commit | `ce03ba1` (develop HEAD at review time) |
| Date | 2026-04-06 |
| Status | COMPLETE |

---

## Summary

The `adapters/postgres` module comprises 9 source files (pool, tx_manager, migrator, outbox_writer, outbox_relay, helpers, errors, embed, doc) and associated tests. This review examines SQL injection vectors, credential management, advisory lock design, transaction safety, connection pool behavior, and information leakage.

**Verdict: 3 P0, 4 P1, 4 P2 findings. Merge-blocking issues exist.**

---

## Findings

### F-01 [P0] SQL Injection via unsanitized tableName in Migrator (6 instances)

**Seat:** S2 Security  
**Category:** SQL Injection  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator.go`  
**Status:** OPEN

**Evidence:**

The `Migrator.tableName` field is a plain `string` accepted from the caller in `NewMigrator` (line 55). It is interpolated directly into SQL via `fmt.Sprintf` without any validation or quoting in 6 locations:

1. **Line 68** (`ensureTable`):
   ```go
   query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (`, m.tableName)
   ```
2. **Line 224** (`appliedVersions`):
   ```go
   query := fmt.Sprintf("SELECT version FROM %s", m.tableName)
   ```
3. **Line 247** (`appliedDetails`):
   ```go
   query := fmt.Sprintf("SELECT version, applied_at FROM %s", m.tableName)
   ```
4. **Line 271** (`latestApplied`):
   ```go
   query := fmt.Sprintf("SELECT version FROM %s ORDER BY version DESC LIMIT 1", m.tableName)
   ```
5. **Line 306** (`applyMigration`):
   ```go
   insertQuery := fmt.Sprintf("INSERT INTO %s (version, name) VALUES ($1, $2)", m.tableName)
   ```
6. **Line 345** (`rollbackMigration`):
   ```go
   deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE version = $1", m.tableName)
   ```

**Risk analysis:**

The `tableName` currently flows from `NewMigrator(pool, fs, tableName)`. All observed call-sites pass hard-coded string literals (`"schema_migrations"`), so the **current runtime risk is low**. However:

- There is **no compile-time or runtime guard** preventing a future caller from passing user-controlled input.
- PostgreSQL identifier parameterization (`$1`) does not work for table names -- `fmt.Sprintf` is the standard Go approach, but it MUST be paired with validation.
- A malicious `tableName` like `"x; DROP TABLE users; --"` would execute arbitrary SQL.

**Fix recommendation (P0 -- must fix before any public API surface):**

Add a `validateIdentifier` function and call it in `NewMigrator`:

```go
import "regexp"

var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)

func validateIdentifier(name string) error {
    if !validIdentifier.MatchString(name) {
        return fmt.Errorf("invalid SQL identifier: %q", name)
    }
    return nil
}
```

Alternatively, use `pgx.Identifier{tableName}.Sanitize()` which properly double-quotes and escapes.

---

### F-02 [P0] Migrator lacks advisory lock -- concurrent migration is unprotected

**Seat:** S2 Security  
**Category:** Data Integrity / Race Condition  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator.go`  
**Status:** OPEN  
**Related:** Issue #21

**Evidence:**

The `doc.go` (line 14) states:
```
ref: Watermill watermill-sql schema_adapter_postgresql.go -- adopted advisory locking for migration init
```

However, **no advisory lock is present anywhere in `migrator.go`**. Grepping for `advisory`, `pg_advisory`, or `pg_try_advisory` across the entire 项目根目录 tree returns zero matches.

The `Up()` method (line 81-105) reads applied versions, then iterates and applies unapplied ones -- without any locking. If two instances start simultaneously:

1. Both read the same `applied` set (e.g., empty).
2. Both attempt to apply migration 001 concurrently.
3. One will fail on the `INSERT INTO schema_migrations (version, name)` due to PRIMARY KEY conflict, but the DDL (e.g., `CREATE TABLE`) may have already partially executed.
4. Worse: if DDL is not idempotent (e.g., `ALTER TABLE ADD COLUMN` without `IF NOT EXISTS`), both instances will fail and leave the schema in an inconsistent state.

**Fix recommendation:**

Acquire a PostgreSQL advisory lock at the beginning of `Up()` and `Down()`:

```go
func (m *Migrator) acquireLock(ctx context.Context) error {
    // Use a fixed lock ID derived from the table name hash.
    _, err := m.pool.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID)
    return err
}

func (m *Migrator) releaseLock(ctx context.Context) {
    _, _ = m.pool.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockID)
}
```

---

### F-03 [P0] Rollback/savepoint uses caller ctx -- fails when ctx is already cancelled (Issue #22)

**Seat:** S2 Security  
**Category:** Transaction Safety / Data Integrity  
**Affected files:**
- `/Users/shengming/Documents/code/gocell/adapters/postgres/tx_manager.go` lines 81, 94, 122, 137
- `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator.go` lines 297, 337
- `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_relay.go` line 142

**Status:** OPEN  
**Related:** Issue #22

**Evidence:**

All rollback operations use the same `ctx` that was passed to the function:

`tx_manager.go` line 81 (panic recovery):
```go
rbErr := tx.Rollback(ctx)
```

`tx_manager.go` line 94 (error path):
```go
if rbErr := tx.Rollback(ctx); rbErr != nil {
```

`tx_manager.go` line 122 (savepoint panic recovery):
```go
_, rbErr := tx.Exec(ctx, fmt.Sprintf("ROLLBACK TO SAVEPOINT %s", spName))
```

When `ctx` is cancelled (e.g., HTTP request timeout), these rollback operations will **also fail** because the context is already done. This leaves the connection in a dirty state: the transaction stays open on the server side until the connection is reclaimed by the pool, potentially holding locks for the full idle timeout (default 5 minutes).

**Severity justification:** Under load, this can cause cascading lock contention and connection pool starvation. A cancelled request that was holding `FOR UPDATE` locks could block other queries for minutes.

**Fix recommendation:**

Use a detached context with a short timeout for rollback operations:

```go
rbCtx, rbCancel := context.WithTimeout(context.Background(), 5*time.Second)
defer rbCancel()
_ = tx.Rollback(rbCtx)
```

Apply this pattern in all 7 rollback/savepoint-rollback sites.

---

### F-04 [P1] Migration version sorting is lexicographic -- breaks at 10+ migrations

**Seat:** S2 Security (Data Integrity)  
**Category:** Migration Ordering  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator.go` lines 196-199  
**Status:** OPEN  
**Related:** Issue #21

**Evidence:**

```go
sort.Slice(files, func(i, j int) bool {
    return files[i].version < files[j].version
})
```

And `latestApplied` (line 271):
```go
query := fmt.Sprintf("SELECT version FROM %s ORDER BY version DESC LIMIT 1", m.tableName)
```

Both use lexicographic (string) ordering. With zero-padded versions (`001`, `002`, ..., `009`), this works. But:
- `"10" < "9"` in string comparison, so migration 10 would be applied before migration 9.
- `"10" < "2"` as well.
- The `ORDER BY version DESC` in SQL also produces incorrect results for non-padded versions.

The current embedded migration set only has `001`, but nothing enforces zero-padding. A future contributor adding `10_add_foo.up.sql` (without `010` padding) will silently produce wrong migration order.

**Fix recommendation:**

Either:
1. Enforce zero-padding via validation in `parseMigrationFilename` (reject versions that don't match `^\d{3,}$`).
2. Parse the version as an integer for sorting: `sort.Slice` by `strconv.Atoi(files[i].version)`.

---

### F-05 [P1] Outbox relay partial-publish + commit creates at-least-once with silent loss risk

**Seat:** S2 Security (Data Integrity)  
**Category:** Outbox Relay Consistency  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_relay.go` lines 186-218  
**Status:** OPEN

**Evidence:**

The `pollOnce` method fetches entries under `FOR UPDATE SKIP LOCKED` within a transaction, then iterates:

```go
for _, e := range entries {
    // ... marshal ...
    if err := r.pub.Publish(ctx, e.RoutingTopic(), payload); err != nil {
        // Do NOT mark as published; will retry on next poll.
        continue   // <-- skips this entry
    }
    // mark as published in DB
}
if err := tx.Commit(ctx); err != nil { ... }
```

The problem: when `Publish` succeeds for entry A but fails for entry B, the loop `continue`s past B. Entry A's `UPDATE ... SET published = true` is buffered in the transaction. Then `tx.Commit()` is called, committing A as published. **But entry B was already published to the broker** -- wait, no, B failed. The issue is different:

If `Publish` succeeds for entry A and its `UPDATE` mark succeeds, but then `Publish` fails for entry B, the transaction still commits, marking A as published. Entry B remains unpublished and will be retried next poll. However, entry B's `FOR UPDATE` lock prevented other relay instances from processing it, so B was effectively skipped for this entire poll cycle. This is correct at-least-once behavior.

**The real issue:** If `tx.Commit()` fails (line 215) after `Publish` succeeded for some entries, those entries were already delivered to the broker but NOT marked as published in the DB. On the next poll, they will be re-fetched and re-published, causing **duplicate delivery**. The outbox pattern should guarantee at-least-once (not exactly-once), so duplicates are expected, but the commit-failure scenario is not logged with enough detail to detect this.

Furthermore, a more subtle problem: the `continue` on publish failure for entry B means the `FOR UPDATE` lock on B is held until `tx.Commit()`, blocking other relay instances from processing B during the entire batch cycle. If the publisher has systematic failures for certain event types, those entries will be indefinitely locked during each poll cycle.

**Fix recommendation:**

1. After `tx.Commit()` failure, log all entry IDs that were successfully published but may be re-delivered.
2. Consider per-entry transactions or savepoints to avoid holding locks on entries that fail to publish.

---

### F-06 [P1] DSN may contain credentials -- no redaction in error messages

**Seat:** S2 Security  
**Category:** Credential Leakage  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/pool.go` lines 98-104  
**Status:** OPEN

**Evidence:**

```go
if cfg.DSN == "" {
    return nil, errcode.New(ErrAdapterPGConnect, "postgres DSN is empty")
}
poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
if err != nil {
    return nil, errcode.Wrap(ErrAdapterPGConnect, "postgres: parse DSN", err)
}
```

The `pgxpool.ParseConfig` error may contain the full DSN including username and password. For example, if the DSN is malformed, pgx returns an error like `cannot parse "postgres://user:secretpass@host..."`. This error is wrapped and could propagate to HTTP responses or external logging systems.

The connection success log (line 121-123) only logs `host` and `max_conns`, which is good. But the error path lacks DSN redaction.

**Fix recommendation:**

Redact credentials from DSN errors before wrapping:

```go
if err != nil {
    return nil, errcode.Wrap(ErrAdapterPGConnect, "postgres: parse DSN failed (check GOCELL_PG_DSN format)", 
        fmt.Errorf("DSN parse error (credentials redacted)"))
}
```

---

### F-07 [P1] OutboxRelay.Start() data race on r.cancel

**Seat:** S2 Security  
**Category:** Concurrency Safety  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_relay.go` lines 74-76, 100-103  
**Status:** OPEN

**Evidence:**

```go
// Start (line 75):
ctx, r.cancel = context.WithCancel(ctx)

// Stop (line 100-103):
r.once.Do(func() {
    if r.cancel != nil {
        r.cancel()
    }
})
```

If `Stop()` is called before `Start()` completes its first line, there is a race on `r.cancel`. The `sync.Once` in `Stop` protects against double-cancel but does NOT protect against the write in `Start`. If `Start` and `Stop` are called from different goroutines (which is the documented usage pattern via `worker.Worker`), the write to `r.cancel` in `Start` and the read in `Stop` constitute a data race.

Additionally, calling `Start()` multiple times would overwrite `r.cancel`, leaking the previous cancel function.

**Fix recommendation:**

Protect `r.cancel` with a mutex, or initialize it in the constructor and use an atomic flag for state management.

---

### F-08 [P2] Savepoint name `sp_N` is predictable -- low risk but not SQL-injection-safe

**Seat:** S2 Security  
**Category:** SQL Construction  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/tx_manager.go` lines 112-114  
**Status:** OPEN

**Evidence:**

```go
spName := fmt.Sprintf("sp_%d", depth)
if _, err := tx.Exec(ctx, fmt.Sprintf("SAVEPOINT %s", spName)); err != nil {
```

The `spName` is constructed from an integer depth counter, so it is safe from injection (integers cannot produce SQL metacharacters). However, the pattern of using `fmt.Sprintf` to construct SQL identifiers without quoting is fragile. If the pattern is copied to other contexts where the name source is less controlled, it becomes a vulnerability.

**Fix recommendation (informational):**

Use `pgx.Identifier{spName}.Sanitize()` for consistency with best practices, or add a comment explicitly noting the safety invariant.

---

### F-09 [P2] `latestApplied` compares error string instead of using pgx sentinel

**Seat:** S2 Security  
**Category:** Error Handling Fragility  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator.go` lines 274-276  
**Status:** OPEN

**Evidence:**

```go
err := m.pool.QueryRow(ctx, query).Scan(&v)
if err != nil {
    if err.Error() == "no rows in result set" {
        return "", nil
    }
```

This relies on an exact string match against pgx's error message, which is an internal implementation detail. If pgx changes the wording in a future version, this check silently breaks -- `latestApplied` would return an error instead of empty string, causing `Down()` to fail.

**Fix recommendation:**

Use the `pgx.ErrNoRows` sentinel:

```go
import "errors"

if errors.Is(err, pgx.ErrNoRows) {
    return "", nil
}
```

---

### F-10 [P2] No connection acquire timeout -- pool exhaustion causes indefinite blocking

**Seat:** S2 Security  
**Category:** Denial of Service / Resource Exhaustion  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/pool.go`  
**Status:** OPEN

**Evidence:**

The pool configuration sets `MaxConns`, `MaxConnIdleTime`, and `MaxConnLifetime` (lines 107-109) but does NOT set `pgxpool.Config.MaxConnIdleTime` or, more importantly, an acquire timeout. The pgxpool default behavior when all connections are in use is to **block indefinitely** until a connection becomes available.

If a burst of requests exhausts the pool (default 10 connections), all subsequent `pool.Acquire()` / `pool.Begin()` / `pool.Query()` calls will block until a connection is freed. Combined with F-03 (rollback on cancelled context fails, holding connections), this creates a realistic DoS vector.

**Fix recommendation:**

Set an explicit acquire timeout in `NewPool`:

```go
poolCfg.MaxConnIdleTime = cfg.IdleTimeout
poolCfg.MaxConnLifetime = cfg.MaxLifetime
// Add:
poolCfg.HealthCheckPeriod = 30 * time.Second
```

pgxpool does respect the caller's context deadline for Acquire, but callers should be advised to always pass a context with timeout.

---

### F-11 [P2] Outbox relay logs entry_id and error details at Error level -- no payload leakage check

**Seat:** S2 Security  
**Category:** Information Leakage  
**Affected file:** `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_relay.go` lines 189-193, 197-201  
**Status:** OPEN

**Evidence:**

```go
slog.Error("outbox relay: marshal entry failed",
    slog.String("entry_id", e.ID),
    slog.Any("error", err),
)
```

and:

```go
slog.Error("outbox relay: publish failed",
    slog.String("entry_id", e.ID),
    slog.String("topic", e.RoutingTopic()),
    slog.Any("error", err),
)
```

The `error` field from `json.Marshal` failures could potentially contain snippets of the entry payload (e.g., in `json: unsupported type` errors that include field names). The publish error from the broker could contain authentication details or broker internal state. These are logged at Error level which is always enabled in production.

This is low severity because entry_id and topic are not sensitive by themselves, and the error content is implementation-dependent.

**Fix recommendation:**

Wrap errors before logging to strip potential sensitive content, or add a comment acknowledging the risk assessment.

---

## Findings Outside Primary Scope (Cell-level repos)

### F-12 [P2] audit_repo.go `Query` builds SQL with string concatenation instead of QueryBuilder

**Seat:** S2 Security  
**Category:** SQL Construction Pattern  
**Affected file:** `/Users/shengming/Documents/code/gocell/cells/audit-core/internal/adapters/postgres/audit_repo.go` lines 117-146  
**Status:** OPEN

**Evidence:**

```go
query := `SELECT ... FROM audit_entries WHERE 1=1`
if filters.EventType != "" {
    query += ` AND event_type = $` + itoa(argIdx)
    args = append(args, filters.EventType)
    argIdx++
}
```

While this is technically safe (filter values are passed as `$N` parameters), the manual index tracking (`argIdx`) and string concatenation pattern is error-prone. The `adapters/postgres` package already provides `QueryBuilder` with `AppendIf` which eliminates both the manual index tracking and the concatenation.

Additionally, line 145 embeds a LIMIT without parameterization:
```go
query += ` ORDER BY timestamp LIMIT ` + itoa(listLimit)
```

This is safe because `listLimit` is a compile-time constant (`1000`), but inconsistent with the parameterized style.

**Fix recommendation:**

Refactor to use `QueryBuilder.AppendIf` for consistency and reduced cognitive load. Note: this would introduce a dependency from `cells/audit-core/internal/adapters/postgres` to `adapters/postgres`, which is currently avoided by design (the cell defines its own DBTX interface). Consider either: (a) moving QueryBuilder to `pkg/`, or (b) keeping the current pattern with a comment explaining the design choice.

---

## Summary Table

| ID | Severity | Category | File | Line(s) | Status |
|---|---|---|---|---|---|
| F-01 | **P0** | SQL Injection | migrator.go | 68, 224, 247, 271, 306, 345 | OPEN |
| F-02 | **P0** | Missing Advisory Lock | migrator.go | 81-105 | OPEN |
| F-03 | **P0** | Rollback on Cancelled ctx | tx_manager.go, migrator.go, outbox_relay.go | 81, 94, 122, 137, 297, 337, 142 | OPEN |
| F-04 | P1 | Version Sort Breakage | migrator.go | 196-199, 271 | OPEN |
| F-05 | P1 | Outbox Partial-Publish | outbox_relay.go | 186-218 | OPEN |
| F-06 | P1 | DSN Credential Leakage | pool.go | 98-104 | OPEN |
| F-07 | P1 | Data Race on cancel | outbox_relay.go | 74-76, 100-103 | OPEN |
| F-08 | P2 | Savepoint Naming | tx_manager.go | 112-114 | OPEN |
| F-09 | P2 | Error String Comparison | migrator.go | 274-276 | OPEN |
| F-10 | P2 | Pool Exhaustion | pool.go | 107-109 | OPEN |
| F-11 | P2 | Log Info Leakage | outbox_relay.go | 189, 197 | OPEN |
| F-12 | P2 | SQL String Concat in Cell | audit_repo.go | 117-146 | OPEN |

**P0 count: 3 (merge-blocking)**  
**P1 count: 4**  
**P2 count: 4**

---

## GoCell Layer Constraint Check

| Check | Result |
|---|---|
| kernel/ imports runtime/adapters/cells? | PASS -- not applicable (reviewing adapters/) |
| cells/ imports adapters/? | PASS -- `audit-core` and `config-core` define their own DBTX interface, no import of `adapters/postgres` |
| Cross-Cell direct import? | PASS -- no cross-cell imports observed |
| CUD operations have consistency level? | **WARN** -- outbox_writer.go performs INSERT (CUD) but does not annotate consistency level in code or comments. The outbox pattern implies L2 (OutboxFact), but this should be explicitly documented. |

---

## Positive Observations

1. **OutboxWriter uses `const` query** (line 49) -- hardcoded table name `outbox_entries`, no injection risk.
2. **All outbox relay SQL queries use `const` with `$N` parameters** -- no injection risk in relay code.
3. **Cell repos define their own DBTX interface** -- proper decoupling from adapter layer.
4. **QueryBuilder helper** provides safe parameterized query construction.
5. **Error codes follow ERR_ADAPTER_PG_ convention** consistently.
6. **Pool logs only host, not full DSN** on successful connection (line 122).
7. **Integration tests** cover pool, tx_manager, migrator, and outbox_writer with real PostgreSQL via testcontainers.
