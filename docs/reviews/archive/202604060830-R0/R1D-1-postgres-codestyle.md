# R1D-1: adapters/postgres Code Style Review

| Field | Value |
|---|---|
| Reviewer Seat | S5 DX/Maintainability |
| Scope | `adapters/postgres/` -- all `.go` source and test files |
| Review Basis Commit | `ce03ba1` (HEAD of develop) |
| Date | 2026-04-06 |

---

## Summary

The `adapters/postgres` module is well-structured: it correctly implements `kernel/outbox` interfaces, uses `errcode` for all external errors, follows structured `slog` logging, and has clean import ordering. Six findings are recorded below (0 P0, 3 P1, 3 P2).

---

## Findings

### F1 -- P1 -- Error code naming inconsistency: `ERR_ADAPTER_NO_TX` breaks prefix convention

**Seat**: S5 DX/Maintainability
**Severity**: P1
**Category**: Naming / errcode consistency
**File**: `adapters/postgres/errors.go:21`
**Evidence**:

```go
// Line 21
ErrAdapterPGNoTx errcode.Code = "ERR_ADAPTER_NO_TX"
```

All other codes in this file use the `ERR_ADAPTER_PG_*` prefix:
- `ERR_ADAPTER_PG_CONNECT`
- `ERR_ADAPTER_PG_QUERY`
- `ERR_ADAPTER_PG_TX_TIMEOUT`
- `ERR_ADAPTER_PG_MIGRATE`
- `ERR_ADAPTER_PG_MARSHAL`
- `ERR_ADAPTER_PG_PUBLISH`

But `ERR_ADAPTER_NO_TX` drops the `PG` segment. Furthermore, the `errcode` central registry in `pkg/errcode/errcode.go:36` defines a **duplicate sentinel** `ErrAdapterNoTx Code = "ERR_ADAPTER_NO_TX"` at the global level. This creates confusion about which constant to reference and whether the code is postgres-specific or adapter-generic.

The `errors_test.go` prefix check only asserts 4 of the 7 codes against `ERR_ADAPTER_PG_`, so this inconsistency slips past testing.

**Recommendation**: Rename to `ERR_ADAPTER_PG_NO_TX` for consistency with the module prefix. If an adapter-generic code is desired, keep the one in `pkg/errcode` and remove the duplicate from this package. Also update the test to cover all 7 codes.

**Status**: OPEN

---

### F2 -- P1 -- String comparison instead of `pgx.ErrNoRows` for empty result detection

**Seat**: S5 DX/Maintainability
**Severity**: P1
**Category**: Error handling fragility
**File**: `adapters/postgres/migrator.go:275`
**Evidence**:

```go
// Line 274-276
err := m.pool.QueryRow(ctx, query).Scan(&v)
if err != nil {
    if err.Error() == "no rows in result set" {
        return "", nil
    }
```

This uses a fragile string comparison to detect the "no rows" case. The `pgx` library provides the sentinel `pgx.ErrNoRows` for exactly this purpose. A future pgx version could change the error message text, silently breaking this check and causing migration operations to return spurious errors.

**Recommendation**: Replace with:
```go
if errors.Is(err, pgx.ErrNoRows) {
    return "", nil
}
```

**Status**: OPEN

---

### F3 -- P1 -- SQL injection surface via unescaped `tableName` in `Migrator`

**Seat**: S5 DX/Maintainability (also S2 Security)
**Severity**: P1
**Category**: Input validation / SQL injection risk
**File**: `adapters/postgres/migrator.go` -- lines 68, 224, 247, 271, 306, 345
**Evidence**:

```go
// Line 68
query := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (`, m.tableName)

// Line 224
query := fmt.Sprintf("SELECT version FROM %s", m.tableName)

// Line 306
"INSERT INTO %s (version, name) VALUES ($1, $2)", m.tableName)

// Line 345
deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE version = $1", m.tableName)
```

The `tableName` is interpolated directly into SQL via `fmt.Sprintf` without any validation or quoting. While `NewMigrator` defaults to `"schema_migrations"`, it accepts arbitrary user input. A caller passing a malicious string (e.g., `"x; DROP TABLE users; --"`) would result in SQL injection.

**Recommendation**: Add an identifier validation in `NewMigrator` (e.g., reject anything not matching `^[a-zA-Z_][a-zA-Z0-9_]*$`), or use `pgx.Identifier{tableName}.Sanitize()` to quote the identifier safely.

**Status**: OPEN

---

### F4 -- P2 -- Unused error code `ErrAdapterPGTxTimeout`

**Seat**: S5 DX/Maintainability
**Severity**: P2
**Category**: Dead code
**File**: `adapters/postgres/errors.go:15`
**Evidence**:

```go
// Line 13-15
// ErrAdapterPGTxTimeout indicates a transaction exceeded its deadline or was
// aborted due to context cancellation.
ErrAdapterPGTxTimeout errcode.Code = "ERR_ADAPTER_PG_TX_TIMEOUT"
```

Grep across all source files (`adapters/postgres/*.go` excluding tests) shows this code is **never referenced** in any `errcode.New` or `errcode.Wrap` call. It exists only in the constant declaration and in the test that checks uniqueness/prefix. `TxManager` does not use it for context timeout errors; it wraps with `ErrAdapterPGConnect` instead.

**Recommendation**: Either use it in `TxManager.RunInTx` when context is cancelled/timed out (to distinguish timeout from connection errors), or remove it to avoid confusion.

**Status**: OPEN

---

### F5 -- P2 -- `OutboxRelay.pollOnce` partial-publish within a single transaction

**Seat**: S5 DX/Maintainability
**Severity**: P2
**Category**: Logic clarity / correctness
**File**: `adapters/postgres/outbox_relay.go:186-213`
**Evidence**:

```go
// Line 186-213
for _, e := range entries {
    payload, err := json.Marshal(e)
    if err != nil {
        slog.Error("outbox relay: marshal entry failed", ...)
        continue   // skip, but tx stays open
    }
    if err := r.pub.Publish(ctx, e.RoutingTopic(), payload); err != nil {
        slog.Error("outbox relay: publish failed", ...)
        continue   // skip, but tx stays open
    }
    const markQuery = `UPDATE outbox_entries SET published = true, published_at = now() WHERE id = $1`
    if _, err := tx.Exec(ctx, markQuery, e.ID); err != nil {
        slog.Error("outbox relay: mark published failed", ...)
    }
}
if err := tx.Commit(ctx); err != nil { ... }
```

The method selects entries with `FOR UPDATE SKIP LOCKED` inside a transaction, then iterates and publishes. If some entries succeed and others fail (publish error), only the successful ones are marked as published, and then the entire batch is committed. This means:

1. Successfully published entries are correctly marked -- good.
2. Failed entries remain locked until commit, blocking other relay instances during the entire batch processing window.
3. If a later `tx.Exec` (mark published) fails but Publish succeeded, the entry will be re-published on the next poll (at-least-once is fine, but worth documenting).

The comment on line 202 says "Do NOT mark as published; will retry on next poll" which is correct for at-least-once, but the dual-continue pattern within a held transaction could benefit from a clearer doc comment explaining the design decision.

**Recommendation**: Add a brief doc comment to `pollOnce` explaining the at-least-once semantics and the deliberate partial-commit-within-batch design. Consider whether failing entries should trigger an early return (releasing locks faster for other relay instances).

**Status**: OPEN

---

### F6 -- P2 -- `errors_test.go` does not cover all error codes

**Seat**: S5 DX/Maintainability
**Severity**: P2
**Category**: Test coverage gap
**File**: `adapters/postgres/errors_test.go:11-16, 26-30`
**Evidence**:

```go
// Line 11-16 (TestErrorCodes_Prefix)
codes := []errcode.Code{
    ErrAdapterPGConnect,
    ErrAdapterPGQuery,
    ErrAdapterPGTxTimeout,
    ErrAdapterPGMigrate,
}
```

The test only checks 4 of 7 declared error codes. Missing from both `TestErrorCodes_Prefix` and `TestErrorCodes_Unique`:
- `ErrAdapterPGNoTx` (`ERR_ADAPTER_NO_TX`)
- `ErrAdapterPGMarshal` (`ERR_ADAPTER_PG_MARSHAL`)
- `ErrAdapterPGPublish` (`ERR_ADAPTER_PG_PUBLISH`)

If `ErrAdapterPGNoTx` had been included in the prefix check, finding F1 would have been caught automatically.

**Recommendation**: Add all 7 codes to the test slices so that future additions are also covered. Consider a generator or reflection-based approach to prevent drift.

**Status**: OPEN

---

## Non-Finding Observations (Positive)

| Area | Assessment |
|---|---|
| **errcode usage** | All non-test code exclusively uses `errcode.New` / `errcode.Wrap`; no bare `errors.New` in production code. Test files use `errors.New` only for simulated failures, which is acceptable. |
| **slog compliance** | All log calls use `slog` with structured fields. Error-level logs include `slog.Any("error", err)` plus business context fields (`entry_id`, `savepoint`, etc.). No `fmt.Println` or `log.Printf` found. |
| **Import ordering** | All files follow the standard Go convention: stdlib, blank line, third-party, blank line, internal. Consistent across all files. |
| **Cognitive complexity** | No function exceeds CC 15. The most complex function is `pollOnce` (~12) with its loop + error branches, still within budget. |
| **DB field naming** | Migration SQL uses `snake_case` (`aggregate_id`, `aggregate_type`, `event_type`, `created_at`, `published_at`). Correct. |
| **ref: tags** | `doc.go` references Watermill `schema_adapter_postgresql.go`, `outbox_writer.go` references Watermill `offset_adapter_postgresql.go`. Both note adopted vs. deviated decisions. |
| **Dependency direction** | `adapters/postgres` imports only `kernel/outbox`, `runtime/worker`, `pkg/errcode`, and `pgx`. No import of `cells/`. Correct per architecture rules. |
| **Interface compliance** | Compile-time checks (`var _ outbox.Writer = (*OutboxWriter)(nil)`, `var _ outbox.Relay = (*OutboxRelay)(nil)`, `var _ worker.Worker = (*OutboxRelay)(nil)`) are present. |
| **Test quality** | Thorough table-driven tests for `QueryBuilder`, `parseMigrationFilename`, `Config`. Mock-based unit tests for relay and writer. Integration tests with testcontainers for pool, txmanager, migrator, and writer. |

---

## Findings Summary

| ID | Severity | File | Category | Summary |
|---|---|---|---|---|
| F1 | P1 | errors.go:21 | Naming | `ERR_ADAPTER_NO_TX` breaks `ERR_ADAPTER_PG_*` prefix; duplicate in pkg/errcode |
| F2 | P1 | migrator.go:275 | Error handling | String comparison `"no rows in result set"` instead of `pgx.ErrNoRows` |
| F3 | P1 | migrator.go (6 sites) | SQL injection | Unvalidated `tableName` interpolated into SQL via `fmt.Sprintf` |
| F4 | P2 | errors.go:15 | Dead code | `ErrAdapterPGTxTimeout` declared but never used |
| F5 | P2 | outbox_relay.go:186-213 | Logic clarity | Partial publish within held transaction; missing design doc |
| F6 | P2 | errors_test.go:11-16 | Test gap | Only 4 of 7 error codes covered in prefix/uniqueness tests |
