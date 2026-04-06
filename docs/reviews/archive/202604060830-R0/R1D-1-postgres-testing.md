# R1D-1: adapters/postgres Testing Review

| Field | Value |
|---|---|
| Reviewer Seat | S3 (Test/Regression) |
| Scope | `src/adapters/postgres/` -- all `*_test.go` files |
| Review Baseline | `ce03ba1` (develop HEAD at review time) |
| Date | 2026-04-06 |

---

## 1. Coverage Analysis

### 1.1 Unit Test Coverage (non-integration, `go test -cover`)

Unit tests are split across 7 test files (excluding `integration_test.go` behind `//go:build integration`).
Without a running PostgreSQL, only the unit tests execute. Coverage is estimated per-file
by mapping each production function to its corresponding test(s).

| Source File | Prod Functions | Unit-Tested Functions | Estimated Unit Coverage |
|---|---|---|---|
| `pool.go` | `ConfigFromEnv`, `applyDefaults`, `NewPool`, `DB`, `Health`, `Close`, `Stats` (7) | `ConfigFromEnv` (4 tests), `applyDefaults` (table-driven), `NewPool` empty/invalid DSN (2 tests). `DB`, `Health`, `Close`, `Stats` require a live pool -- tested only in integration. | ~45% (3/7 fully unit-tested; NewPool partial -- only error paths) |
| `tx_manager.go` | `CtxWithTx`, `TxFromContext`, `savepointDepth`, `withSavepointDepth`, `NewTxManager`, `RunInTx`, `runInSavepoint` (7) | All 7 covered via mocks. `RunInTx` top-level path tested only in integration (needs real pool.Begin). Savepoint path thoroughly tested via mockTx. | ~75% (top-level tx BEGIN path is integration-only) |
| `migrator.go` | `NewMigrator`, `ensureTable`, `Up`, `Down`, `Status`, `listMigrations`, `parseMigrationFilename`, `appliedVersions`, `appliedDetails`, `latestApplied`, `applyMigration`, `rollbackMigration` (12) | `NewMigrator` (2 tests), `listMigrations` (2 tests), `parseMigrationFilename` (table-driven, 7 cases). DB-touching functions (`ensureTable`, `Up`, `Down`, `Status`, `appliedVersions`, `appliedDetails`, `latestApplied`, `applyMigration`, `rollbackMigration`) are integration-only. | ~25% unit / ~85% with integration |
| `outbox_writer.go` | `NewOutboxWriter`, `Write` (2) | Both fully unit-tested (4 tests: no-tx, success, zero-createdAt, exec-error) | ~100% |
| `outbox_relay.go` | `DefaultRelayConfig`, `NewOutboxRelay`, `Start`, `Stop`, `pollLoop`, `pollOnce`, `cleanupLoop`, `deletePublishedBefore` (8) | `DefaultRelayConfig` (1), `Start`/`Stop` (1), `pollOnce` (3). `cleanupLoop` and `deletePublishedBefore` have **zero** unit test coverage. `pollLoop` is indirectly tested via `Start`/`Stop`. | ~55% |
| `helpers.go` | `NewQueryBuilder`, `Append`, `AppendParam`, `AppendIf`, `Build`, `Args`, `SQL`, `NextParam`, `Reset` (9) | All 9 functions covered (8 tests) | ~100% |
| `errors.go` | Constants only (no functions) | Prefix, uniqueness, creation tests (3 tests) | ~100% |
| `embed.go` | `MigrationsFS` (1) | Tested via `TestMigrationsFS_SubDirectory` | ~100% |

**Aggregate unit-only estimate: ~60-65%** (many DB-touching paths are integration-only).

**Aggregate with integration: ~85-90%** (integration_test.go covers Pool, TxManager, Migrator, OutboxWriter against real Postgres).

### 1.2 Integration Test Coverage (`//go:build integration`)

The `integration_test.go` file uses **testcontainers** (postgres:15-alpine) with a proper skip guard (`//go:build integration`). It covers:

- **T19 Pool**: connect, Health(), Stats(), Close()
- **T20 TxManager**: commit path, rollback on error, rollback on panic
- **T21 Migrator**: Up (idempotent), Status, Down
- **T22 OutboxWriter**: write-in-tx, write-without-tx, write-rolled-back-in-failed-tx

**Missing integration coverage**: OutboxRelay full-chain (poll -> publish -> mark sent -> cleanup) is NOT tested in integration.

---

## 2. Findings

### F-R1D1-01 [P1] OutboxRelay `deletePublishedBefore` has zero test coverage

- **Seat**: S3 (Test/Regression)
- **Severity**: P1
- **Category**: Missing test coverage
- **File**: `src/adapters/postgres/outbox_relay.go:250-262`, `src/adapters/postgres/outbox_relay_test.go`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: The function `deletePublishedBefore` (lines 250-262 of `outbox_relay.go`) and its caller `cleanupLoop` (lines 224-247) have no unit tests. Searching all test files for `deletePublished` or `cleanup` in test function names returns zero matches.

**Impact**: The cleanup path -- which deletes old published outbox entries -- is completely untested. A regression in the DELETE query or retention logic would go undetected.

**Recommendation**: Add a unit test for `deletePublishedBefore` using `mockDBTX`. Test cases:
1. No rows deleted (returns cleanly)
2. Rows deleted (verify SQL and cutoff argument)
3. DB error (verify errcode wrapping)

---

### F-R1D1-02 [P1] OutboxRelay integration test missing -- poll/publish/mark-sent chain untested against real DB

- **Seat**: S3 (Test/Regression)
- **Severity**: P1
- **Category**: Missing integration test
- **File**: `src/adapters/postgres/integration_test.go`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: `integration_test.go` contains T19 (Pool), T20 (TxManager), T21 (Migrator), T22 (OutboxWriter) but no T23 for OutboxRelay. The relay's `pollOnce` involves `FOR UPDATE SKIP LOCKED` and transactional commit semantics that unit mocks cannot validate.

**Impact**: The critical outbox relay chain (fetch unpublished -> publish -> mark published -> commit) is only tested via unit mocks that do not exercise real SQL behavior. Bugs in the `FOR UPDATE SKIP LOCKED` query, transaction commit ordering, or the mark-published UPDATE would be missed.

**Recommendation**: Add `TestIntegration_OutboxRelay` that:
1. Inserts an outbox entry via OutboxWriter+TxManager
2. Calls `pollOnce` with a mockPublisher
3. Verifies the entry is marked `published = true` in the database
4. Verifies `deletePublishedBefore` with a past cutoff removes the entry

---

### F-R1D1-03 [P1] Migrator: no test for advisory lock / concurrent migration safety

- **Seat**: S3 (Test/Regression)
- **Severity**: P1
- **Category**: Missing test -- concurrency
- **File**: `src/adapters/postgres/migrator.go`, `src/adapters/postgres/migrator_test.go`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: The `doc.go` (line 8) and `Migrator` struct comment (line 41 of migrator.go) explicitly state: "advisory locking to prevent concurrent execution (adopted from Watermill's approach)". However, examining `migrator.go` lines 67-361, there is **no advisory lock implementation**. The `applyMigration` and `rollbackMigration` functions use plain `pool.Begin(ctx)` without acquiring an advisory lock. Additionally, no tests exist for concurrent migration safety.

**Impact**: Two simultaneous `migrator.Up()` calls could both attempt to apply the same migration, causing a primary key conflict or duplicate DDL execution. The doc claims a feature that does not exist in the code.

**Recommendation**:
1. Either implement advisory locking (`SELECT pg_advisory_lock(hash)`) in `Up`/`Down` before iterating migrations, or
2. Remove the advisory lock claim from the doc.go/struct comment.
3. Add a concurrent integration test that runs `migrator.Up()` from two goroutines simultaneously to verify safety.

---

### F-R1D1-04 [P2] Error codes test in `errors_test.go` misses `ErrAdapterPGNoTx`, `ErrAdapterPGMarshal`, `ErrAdapterPGPublish`

- **Seat**: S3 (Test/Regression)
- **Severity**: P2
- **Category**: Incomplete test coverage
- **File**: `src/adapters/postgres/errors_test.go:11-15` and `:26-30`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: The error codes slice in both `TestErrorCodes_Prefix` and `TestErrorCodes_Unique` contains only 4 codes:
```go
codes := []errcode.Code{
    ErrAdapterPGConnect,
    ErrAdapterPGQuery,
    ErrAdapterPGTxTimeout,
    ErrAdapterPGMigrate,
}
```
But `errors.go` defines 7 codes total -- the tests omit `ErrAdapterPGNoTx`, `ErrAdapterPGMarshal`, and `ErrAdapterPGPublish`.

**Impact**: If someone renames or removes one of the 3 missing codes, the prefix/uniqueness tests would not catch it.

**Recommendation**: Add all 7 codes to both test slices.

---

### F-R1D1-05 [P2] `mockRows.Scan` does not handle all pgx column types

- **Seat**: S3 (Test/Regression)
- **Severity**: P2
- **Category**: Test infrastructure fragility
- **File**: `src/adapters/postgres/outbox_relay_test.go:207-221`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: The `mockRows.Scan` method (lines 207-221) only handles `*string`, `*[]byte`, and `*time.Time` via type switch. If a future test or production code change introduces a new column type (e.g., `*int64`, `*bool`), the mock will silently skip assignment, leading to hard-to-diagnose zero-value assertions.

```go
func (r *mockRows) Scan(dest ...any) error {
    row := r.entries[r.idx]
    r.idx++
    for i, v := range row.values {
        switch d := dest[i].(type) {
        case *string:
            *d = v.(string)
        case *[]byte:
            *d = v.([]byte)
        case *time.Time:
            *d = v.(time.Time)
        }
        // No default case -- silently skips unrecognized types
    }
    return nil
}
```

**Recommendation**: Add a `default` case that calls `t.Fatalf` or returns an error for unsupported types, so new column types cause a clear test failure rather than silent zero-values.

---

### F-R1D1-06 [P2] `TestOutboxRelay_PollOnce_PublishesEntries` does not verify commit was called

- **Seat**: S3 (Test/Regression)
- **Severity**: P2
- **Category**: Incomplete assertion
- **File**: `src/adapters/postgres/outbox_relay_test.go:55-93`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: The test verifies that entries were published and that the UPDATE was issued (line 92: `assert.Contains(t, db.execCalls[0].sql, "UPDATE outbox_entries SET published = true")`), but the `mockRelayTx.Commit` (line 174) does not track whether it was called. The `pollOnce` function's transactional guarantee (commit after marking entries) is not asserted.

**Recommendation**: Add a `committed bool` field to `mockRelayTx`, set it in `Commit()`, and assert `committed == true` after `pollOnce` returns.

---

### F-R1D1-07 [P2] TxManager unit tests do not cover top-level (non-savepoint) begin/commit/rollback

- **Seat**: S3 (Test/Regression)
- **Severity**: P2
- **Category**: Missing unit test path
- **File**: `src/adapters/postgres/tx_manager_test.go`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: All `TestRunInTx_*` unit tests pre-populate the context with a transaction (`CtxWithTx`), so they exercise only the savepoint path (`runInSavepoint`). The top-level path (lines 69-107 of `tx_manager.go`) -- `pool.Begin` -> `fn(txCtx)` -> `Commit`/`Rollback` -- is tested only via integration tests. This means the commit error wrapping (line 104) and the panic-recovery defer (lines 79-89) for top-level transactions are not unit-testable.

**Impact**: Not severe given integration coverage, but a mock `pgxpool.Pool` or interface extraction could enable pure unit tests for the top-level path.

**Recommendation**: Consider extracting a `poolBeginner` interface (just `Begin(ctx) (pgx.Tx, error)`) so the top-level path can be unit-tested with a mock, similar to how `relayDB` was extracted for OutboxRelay.

---

### F-R1D1-08 [P2] Table-driven test pattern underutilized for `ConfigFromEnv` tests

- **Seat**: S3 (Test/Regression)
- **Severity**: P2
- **Category**: Test style
- **File**: `src/adapters/postgres/pool_test.go:12-60`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: `TestConfigFromEnv_Defaults`, `TestConfigFromEnv_CustomValues`, `TestConfigFromEnv_InvalidValues`, and `TestConfigFromEnv_NegativeMaxConns` are 4 separate test functions that follow the same pattern (set env vars, call ConfigFromEnv, assert). In contrast, `TestConfig_ApplyDefaults` (line 62) correctly uses a table-driven approach. Per CLAUDE.md, "kernel/ layer >= 90% (table-driven test)" -- while adapters are not held to the kernel standard, the project convention favors table-driven tests.

**Recommendation**: Consolidate the 4 `ConfigFromEnv` tests into a single table-driven test for consistency with the project convention.

---

### F-R1D1-09 [P2] OutboxWriter unit test does not test nil/empty Metadata serialization

- **Seat**: S3 (Test/Regression)
- **Severity**: P2
- **Category**: Missing edge case
- **File**: `src/adapters/postgres/outbox_writer_test.go`
- **Baseline**: `ce03ba1`
- **Status**: OPEN

**Evidence**: `TestOutboxWriter_Write_Success` tests with `Metadata: map[string]string{"source": "test"}` and `TestOutboxWriter_Write_ZeroCreatedAt` passes `Metadata: nil` implicitly but does not assert the serialized metadata value. The `json.Marshal(nil map)` produces `"null"` while `json.Marshal(map[string]string{})` produces `"{}"`. Since the outbox_entries table has `metadata JSONB DEFAULT '{}'`, a `null` value might cause issues downstream.

**Recommendation**: Add explicit test cases for:
1. `Metadata: nil` -- assert serialized to `"null"` and verify this is acceptable
2. `Metadata: map[string]string{}` -- assert serialized to `"{}"`

---

## 3. Summary

### Positive Observations

1. **Testcontainers usage**: `integration_test.go` correctly uses `testcontainers-go` with the postgres module and a proper `//go:build integration` guard. The `setupPostgres` helper is clean and reusable.

2. **Mock quality**: The mocks (`mockTx`, `mockOutboxTx`, `mockDBTX`, `mockRelayTx`, `mockRows`, `mockPublisher`) are well-structured, using embedded interfaces for pgx.Tx compliance while only overriding needed methods.

3. **Table-driven tests present**: `TestParseMigrationFilename` (7 cases), `TestConfig_ApplyDefaults` (4 cases), and `TestQueryBuilder_AppendIf` (2 cases) demonstrate good table-driven patterns.

4. **OutboxWriter tests are thorough**: All 4 paths (no-tx, success, zero-createdAt, exec-error) are covered with proper error code assertions.

5. **Savepoint testing is excellent**: `TestRunInTx_NestedSavepoints` validates double-nesting with exact SQL sequence verification. Panic recovery is also tested.

6. **Integration tests cover critical paths**: TxManager commit/rollback/panic against real DB, Migrator Up/Down/Status lifecycle, OutboxWriter transactional atomicity verification (write-in-failed-tx proves rollback).

### Coverage Assessment

| Target | Threshold | Actual (Unit) | Actual (Unit + Integration) | Verdict |
|---|---|---|---|---|
| adapters/postgres | >= 80% | ~60-65% | ~85-90% | PASS with integration, FAIL unit-only |

### Finding Summary

| ID | Severity | Category | File |
|---|---|---|---|
| F-R1D1-01 | P1 | Missing test: `deletePublishedBefore` | outbox_relay_test.go |
| F-R1D1-02 | P1 | Missing integration: OutboxRelay chain | integration_test.go |
| F-R1D1-03 | P1 | Missing: advisory lock impl + concurrent migration test | migrator.go |
| F-R1D1-04 | P2 | Incomplete error codes in test | errors_test.go |
| F-R1D1-05 | P2 | mockRows.Scan silent type skip | outbox_relay_test.go |
| F-R1D1-06 | P2 | Missing commit assertion | outbox_relay_test.go |
| F-R1D1-07 | P2 | Top-level TxManager path unit-untestable | tx_manager_test.go |
| F-R1D1-08 | P2 | ConfigFromEnv not table-driven | pool_test.go |
| F-R1D1-09 | P2 | Missing nil/empty Metadata edge case | outbox_writer_test.go |

**P0: 0 | P1: 3 | P2: 6 | Total: 9**
