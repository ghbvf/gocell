# R1D-1: adapters/postgres -- Kernel Guardian Review

**Reviewer role**: Kernel Guardian
**Scope**: `adapters/postgres/` (13 .go files + 2 .sql migrations)
**Date**: 2026-04-06

---

## 1. File Inventory

| File | LOC (approx) | Purpose |
|------|-------------|---------|
| doc.go | 17 | Package doc |
| errors.go | 28 | Error code constants (7 codes) |
| errors_test.go | 43 | Prefix / uniqueness tests (4/7 codes covered) |
| pool.go | 160 | pgxpool wrapper, Config, Health, Stats |
| pool_test.go | 133 | Unit + env var tests |
| tx_manager.go | 152 | RunInTx, savepoint nesting, panic rollback |
| tx_manager_test.go | 178 | Savepoint create/release/rollback, panic |
| helpers.go | 87 | RowScanner, QueryBuilder |
| helpers_test.go | 120 | Builder smoke tests |
| migrator.go | 362 | embed.FS-driven SQL migration Up/Down/Status |
| migrator_test.go | 149 | Filename parsing, FS listing, default table |
| embed.go | 26 | go:embed migrations/*.sql |
| outbox_writer.go | 68 | outbox.Writer implementation |
| outbox_writer_test.go | 134 | No-tx, success, zero-time, exec-error |
| outbox_relay.go | 263 | outbox.Relay + worker.Worker implementation |
| outbox_relay_test.go | 251 | Start/Stop, poll, publish error |
| integration_test.go | 335 | Testcontainers: Pool, TxManager, Migrator, OutboxWriter |
| migrations/001_create_outbox_entries.up.sql | 14 | DDL for outbox_entries table |
| migrations/001_create_outbox_entries.down.sql | 2 | DROP TABLE |

---

## 2. Layering / Dependency Isolation

### 2.1 Import Analysis (production .go only, excluding _test.go)

| Source File | Internal Imports | Verdict |
|-------------|-----------------|---------|
| errors.go | `pkg/errcode` | OK -- adapter -> pkg |
| pool.go | `pkg/errcode`, `pgxpool` | OK |
| tx_manager.go | `pkg/errcode`, `pgx`, `pgxpool` | OK |
| helpers.go | `pgx` | OK |
| migrator.go | `pkg/errcode`, `pgxpool` | OK |
| embed.go | (stdlib only) | OK |
| outbox_writer.go | `kernel/outbox`, `pkg/errcode` | OK -- adapter -> kernel + pkg |
| outbox_relay.go | `kernel/outbox`, `pkg/errcode`, **`runtime/worker`** | **KNOWN VIOLATION** |
| doc.go | (none) | OK |

**Finding [F-01] (YELLOW -- known)**: `outbox_relay.go` imports `runtime/worker` to satisfy the `worker.Worker` interface. Per the GoCell dependency rule, `adapters/` should only implement interfaces defined in `kernel/` or `runtime/`. The import is to implement a `runtime/`-defined interface, which is explicitly permitted by the rule: "adapters/ implements kernel/ or runtime/ defined interfaces." **Re-assessment: this is actually compliant.** The `worker.Worker` interface is defined in `runtime/worker`, and `OutboxRelay` implements it. The adapter depends on the interface definition, not on runtime business logic.

**Verdict: No true layering violation found.** The adapter correctly depends downward: `adapters/ -> kernel/`, `adapters/ -> pkg/`, `adapters/ -> runtime/` (interface only). No imports of `cells/` or upward cross-cutting.

### 2.2 Forbidden imports absent

No imports of:
- `cells/*` -- confirmed absent
- `adapters/*` (other adapter packages) -- confirmed absent
- `cmd/*` -- confirmed absent

---

## 3. Kernel Interface Compliance

### 3.1 outbox.Writer

**Kernel contract** (`kernel/outbox/outbox.go:38-44`):
```go
type Writer interface {
    Write(ctx context.Context, entry Entry) error
}
```

**Adapter implementation** (`outbox_writer.go:33`):
```go
func (w *OutboxWriter) Write(ctx context.Context, entry outbox.Entry) error
```

Compile-time check present at line 14:
```go
var _ outbox.Writer = (*OutboxWriter)(nil)
```

**Assessment**: Signature matches. Compile-time check present. **PASS**.

### 3.2 outbox.Relay

**Kernel contract** (`kernel/outbox/outbox.go:47-50`):
```go
type Relay interface {
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
}
```

**Adapter implementation** (`outbox_relay.go:74, 99`):
```go
func (r *OutboxRelay) Start(ctx context.Context) error
func (r *OutboxRelay) Stop(_ context.Context) error
```

Compile-time checks present at lines 19-21:
```go
var (
    _ outbox.Relay  = (*OutboxRelay)(nil)
    _ worker.Worker = (*OutboxRelay)(nil)
)
```

**Assessment**: Both `outbox.Relay` and `worker.Worker` signatures match. Compile-time checks present. **PASS**.

### 3.3 outbox.Publisher (consumed, not implemented)

`OutboxRelay` accepts `outbox.Publisher` as a constructor dependency (line 64), correctly depending on the kernel-defined interface rather than a concrete type. **PASS**.

---

## 4. Topic Field / RoutingTopic() Analysis

### 4.1 Entry.Topic not persisted in database

**Finding [F-02] (RED -- data loss risk)**:

The `outbox.Entry` struct defines a `Topic` field (line 20 of `outbox.go`), and `RoutingTopic()` uses it with EventType as fallback. However:

1. **OutboxWriter.Write** (`outbox_writer.go:49-51`) INSERT query:
   ```sql
   INSERT INTO outbox_entries
     (id, aggregate_id, aggregate_type, event_type, payload, metadata, created_at, published)
     VALUES ($1, $2, $3, $4, $5, $6, $7, false)
   ```
   The `topic` column is **absent** from both the column list and the values.

2. **Migration DDL** (`001_create_outbox_entries.up.sql`) has no `topic` column.

3. **OutboxRelay.pollOnce** SELECT query (`outbox_relay.go:146-152`) does not select `topic`:
   ```sql
   SELECT id, aggregate_id, aggregate_type, event_type,
     payload, metadata, created_at
   ```

**Impact**: If a caller sets `entry.Topic = "orders.v2"` (distinct from `entry.EventType = "order.created"`), the Writer discards `Topic` silently. When the Relay later fetches the entry, `e.Topic` is zero-valued, so `RoutingTopic()` falls back to `EventType`. The message is published to the **wrong routing key**.

This is functionally correct only if Topic is never set differently from EventType, but the kernel API explicitly offers Topic as an override mechanism. The adapter silently breaks that contract.

**Severity**: P1 -- Silent data loss when Topic != EventType. The RoutingTopic() fallback masks the problem, making it very hard to diagnose.

**Remediation**:
- Add a `topic` TEXT column to `outbox_entries` (migration 002).
- Include `topic` in the INSERT (outbox_writer.go) and SELECT (outbox_relay.go) queries.
- Alternatively, if Topic is intentionally not persisted, document this decision and add a runtime guard that returns an error if `entry.Topic != ""` and `entry.Topic != entry.EventType`.

### 4.2 RoutingTopic() usage in Relay is correct *given the data it receives*

Line 196 of `outbox_relay.go`:
```go
r.pub.Publish(ctx, e.RoutingTopic(), payload)
```

This correctly uses `RoutingTopic()` rather than hard-coding `EventType`. The logic is sound; the problem is solely that `Topic` is lost at the persistence layer.

---

## 5. Transaction Semantics (L1/L2 support)

### 5.1 L1 (LocalTx) -- TxManager.RunInTx

- Begins a real pgx transaction, stores it in context via `CtxWithTx`.
- Supports savepoint nesting for re-entrant calls.
- Panic recovery rolls back before re-panicking.
- On error, explicit `tx.Rollback()`.
- On success, `tx.Commit()`.

**Assessment**: L1 semantics are fully covered. **PASS**.

### 5.2 L2 (OutboxFact) -- OutboxWriter + TxManager

The L2 pattern requires: business state write + outbox entry write in the **same** database transaction.

- `OutboxWriter.Write` calls `TxFromContext(ctx)` to obtain the tx, then executes the INSERT on that tx.
- The caller is expected to call `writer.Write(txCtx, entry)` inside `txm.RunInTx(...)`.
- Integration test `TestIntegration_OutboxWriter/write_in_tx` confirms this works end-to-end.
- Integration test `TestIntegration_OutboxWriter/write_rolled_back_in_failed_tx` confirms rollback atomicity.

**Assessment**: L2 atomicity guarantee is architecturally sound. The outbox write shares the caller's transaction. **PASS**.

### 5.3 L2 Relay -- at-least-once delivery

`OutboxRelay.pollOnce`:
- Uses `FOR UPDATE SKIP LOCKED` to prevent double-processing under concurrent relay instances.
- Publishes entries one by one; on publish failure, the entry is **not** marked as published (retry on next poll).
- Marks published entries only after successful `Publish()`.
- Commits the entire batch.

**Finding [F-03] (YELLOW -- partial-commit risk)**:
The relay publishes entries in a loop, marking each individually within a single transaction. If entry A publishes successfully and is marked, but entry B fails to publish, then on `tx.Commit()` entry A is marked published while B is not. This is correct for at-least-once semantics.

However, if `tx.Commit()` fails after marking entries, the published entries revert to unpublished state but the messages were already sent to the broker, leading to duplicate delivery. This is inherent to the outbox pattern (at-least-once, not exactly-once) and is acceptable. **No action required.**

---

## 6. errcode Usage

### 6.1 Production code -- NO bare errors.New

Confirmed: all production files (`errors.go`, `pool.go`, `tx_manager.go`, `migrator.go`, `outbox_writer.go`, `outbox_relay.go`, `helpers.go`, `embed.go`, `doc.go`) use `errcode.New()` or `errcode.Wrap()` exclusively. Zero instances of `errors.New` in non-test code. **PASS**.

### 6.2 Test code -- errors.New acceptable

`integration_test.go` uses `errors.New` three times, all for simulating test failures. These do not cross package boundaries. **Acceptable**.

### 6.3 Error code prefix compliance

**Finding [F-04] (YELLOW -- test coverage gap)**:

`errors_test.go` (`TestErrorCodes_Prefix` and `TestErrorCodes_Unique`) only covers 4 of 7 declared error codes:
- Covered: `ErrAdapterPGConnect`, `ErrAdapterPGQuery`, `ErrAdapterPGTxTimeout`, `ErrAdapterPGMigrate`
- Missing: `ErrAdapterPGNoTx`, `ErrAdapterPGMarshal`, `ErrAdapterPGPublish`

Additionally, `ErrAdapterPGNoTx` has an inconsistent code string:
```go
ErrAdapterPGNoTx errcode.Code = "ERR_ADAPTER_NO_TX"  // missing "PG_" segment
```
All other codes follow the `ERR_ADAPTER_PG_*` pattern. The test would catch this if the code were included.

**Severity**: P2 -- Naming inconsistency; does not affect runtime but violates the `ERR_ADAPTER_PG_*` convention stated in `doc.go`.

**Remediation**:
1. Rename `"ERR_ADAPTER_NO_TX"` to `"ERR_ADAPTER_PG_NO_TX"`.
2. Add all 7 error codes to both `TestErrorCodes_Prefix` and `TestErrorCodes_Unique`.

---

## 7. Metadata Compliance (cell.yaml / slice.yaml)

Adapter packages do not have cell.yaml or slice.yaml files, which is correct. Adapters are not Cells; they are implementation modules consumed by Cells via dependency injection. **N/A -- compliant by design**.

---

## 8. SQL Injection Surface Audit

### 8.1 Migrator tableName

`migrator.go` constructs SQL using `fmt.Sprintf` with `m.tableName` (lines 68, 224, 246, 271, 306, 346). The `tableName` is set at construction time by the caller (default: `"schema_migrations"`). If an untrusted string is passed, SQL injection is possible.

**Finding [F-05] (YELLOW -- low risk)**:
No runtime user input reaches `tableName` in current usage (it is always a compile-time constant). However, the API accepts an arbitrary string without validation.

**Remediation** (defensive): Add a regex guard in `NewMigrator` that rejects table names not matching `^[a-z_][a-z0-9_]*$`.

### 8.2 Savepoint names

`tx_manager.go` constructs savepoint names via `fmt.Sprintf("sp_%d", depth)` where `depth` is an integer. No injection risk. **PASS**.

---

## 9. Outbox Migration DDL Review

`001_create_outbox_entries.up.sql`:
```sql
CREATE TABLE IF NOT EXISTS outbox_entries (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    aggregate_id   TEXT NOT NULL,
    aggregate_type TEXT NOT NULL,
    event_type     TEXT NOT NULL,
    payload        JSONB NOT NULL,
    metadata       JSONB DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at   TIMESTAMPTZ,
    published      BOOLEAN NOT NULL DEFAULT false
);
CREATE INDEX IF NOT EXISTS idx_outbox_unpublished ON outbox_entries (created_at) WHERE published = false;
```

**Observations**:
- Partial index `idx_outbox_unpublished` correctly covers the relay's `WHERE published = false ORDER BY created_at` query. **Good**.
- `id` defaults to `gen_random_uuid()` but the Writer always provides an explicit ID, so the default is a safety net only.
- No `topic` column (see F-02).

---

## 10. Findings Summary

| ID | Severity | Category | Description |
|----|----------|----------|-------------|
| F-01 | GREEN (reclassified) | Layering | `outbox_relay.go` imports `runtime/worker` -- permitted; implements runtime-defined interface |
| **F-02** | **RED / P1** | **Contract compliance** | **Entry.Topic not persisted; RoutingTopic() silently falls back, publishing to wrong key when Topic differs from EventType** |
| F-03 | GREEN | Transaction semantics | At-least-once delivery with inherent duplicate risk is acceptable for outbox pattern |
| F-04 | YELLOW / P2 | errcode compliance | `ErrAdapterPGNoTx` code string `"ERR_ADAPTER_NO_TX"` breaks `ERR_ADAPTER_PG_*` convention; `errors_test.go` only covers 4/7 codes |
| F-05 | YELLOW / P3 | Defensive coding | `Migrator.tableName` unvalidated (low risk; no runtime user input reaches it currently) |

---

## 11. Recommendations

### Must Fix (before merge)

1. **[F-02] Persist the Topic field**: Add migration `002_add_topic_column.up.sql` with `ALTER TABLE outbox_entries ADD COLUMN topic TEXT NOT NULL DEFAULT ''`. Update `OutboxWriter.Write` INSERT to include `topic`. Update `OutboxRelay.pollOnce` SELECT to read `topic` into `e.Topic`. This closes the silent data loss path.

### Should Fix (next sprint)

2. **[F-04] Fix error code naming**: Rename `"ERR_ADAPTER_NO_TX"` -> `"ERR_ADAPTER_PG_NO_TX"` and update all 7 codes in `errors_test.go` prefix/uniqueness tests.

### Nice to Have

3. **[F-05] Validate Migrator.tableName**: Add a regex guard in `NewMigrator`.

---

## 12. Dimensional Assessment (adapter sub-module)

| Dimension | Score | Evidence |
|-----------|-------|---------|
| A. Layering compliance | GREEN | All imports verified; adapter -> kernel + pkg + runtime(interface). No upward deps. |
| B. Interface fidelity | YELLOW | Writer/Relay interfaces satisfied at compile time; but Topic field not round-tripped (F-02). |
| C. Error handling | YELLOW | errcode used throughout production code; naming inconsistency in ErrAdapterPGNoTx (F-04). |
| D. Transaction correctness | GREEN | L1 (RunInTx) and L2 (outbox atomic write) semantics are sound; integration-tested. |
| E. Test coverage | GREEN | Unit tests for all components; integration tests with testcontainers. Minor gap in error code test (F-04). |
| F. Schema integrity | YELLOW | DDL well-structured with partial index; but Topic column missing (F-02). |

---

*Review generated by Kernel Guardian. Findings referenced by absolute paths under `/Users/shengming/Documents/code/gocell/adapters/postgres/`.*
