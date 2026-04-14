# R1D-1: adapters/postgres Product Acceptance Review

- **Reviewer role**: Product Manager (consumer-facing quality)
- **Review baseline commit**: `ce03ba1` (HEAD of develop at review time)
- **Review date**: 2026-04-06
- **Persona**: Go developer integrating GoCell's PostgreSQL adapter via `go get`
- **Review dimensions**: Functionality, API usability, Configuration, Observability, Documentation, Cross-adapter consistency

---

## Review Scope

| File | Role | LOC (approx) |
|------|------|-------------|
| `doc.go` | Package documentation | 16 |
| `pool.go` | Connection pool (Config + Pool) | 160 |
| `tx_manager.go` | Transaction manager with savepoints | 152 |
| `migrator.go` | SQL migration engine (up/down/status) | 362 |
| `outbox_writer.go` | Transactional outbox writer | 69 |
| `outbox_relay.go` | Poll-and-publish relay | 263 |
| `errors.go` | Error code constants | 29 |
| `helpers.go` | RowScanner + QueryBuilder | 87 |
| `embed.go` | Embedded migration FS | 26 |
| `migrations/001_create_outbox_entries.up.sql` | Outbox table DDL | 14 |
| `migrations/001_create_outbox_entries.down.sql` | Outbox table drop | 2 |
| **Test files** (7 files) | Unit + integration | ~580 |
| **Total source** | **9 files** | **~1,180** |

### Downstream consumers examined

- `cells/audit-core/internal/adapters/postgres/audit_repo.go` -- defines own DBTX interface (does NOT import adapter)
- `cells/config-core/internal/adapters/postgres/config_repo.go` -- same pattern
- `runtime/worker/worker.go` -- OutboxRelay implements `worker.Worker`
- `kernel/outbox/outbox.go` -- OutboxWriter implements `outbox.Writer`; OutboxRelay implements `outbox.Relay`
- `examples/todo-order`, `examples/iot-device`, `examples/sso-bff` -- none use the postgres adapter

---

## 1. Functional Completeness Assessment

### 1.1 Component inventory

| Component | Status | Notes |
|-----------|--------|-------|
| **Pool** (connect, health, close, stats) | PRESENT | Wraps `pgxpool.Pool` with env-based config |
| **TxManager** (RunInTx, savepoint nesting, panic safety) | PRESENT | Context-embedded tx pattern |
| **Migrator** (up, down, status, embed.FS source) | PRESENT | Per-migration transactions, tracking table |
| **OutboxWriter** (transactional insert) | PRESENT | Fail-fast on missing tx |
| **OutboxRelay** (poll-publish-cleanup) | PRESENT | FOR UPDATE SKIP LOCKED, retention cleanup |
| **Error codes** (7 codes) | PRESENT | ERR_ADAPTER_PG_* prefix |
| **Helpers** (RowScanner, QueryBuilder) | PRESENT | Reduce boilerplate for repos |
| **Embedded migrations** | PRESENT | 001_create_outbox_entries |

### 1.2 Missing capabilities

| ID | Missing Capability | Severity | Rationale |
|----|-------------------|----------|-----------|
| **F01** | `[验收标准缺失]` Advisory lock for Migrator | P2 | doc.go claims "adopted advisory locking for migration init" (line 14), but the actual Migrator code contains NO advisory lock acquisition. Concurrent `Up()` calls from multiple pods would race. |
| **F02** | `[功能完整度]` No `ConfigFromEnv` for OutboxRelay config | P3 | Pool has `ConfigFromEnv()` but RelayConfig has only `DefaultRelayConfig()`. Developer must construct `RelayConfig` manually -- minor inconsistency. |
| **F03** | `[验收标准缺失]` No outbox `Topic` column in DDL | P2 | `kernel/outbox.Entry` has a `Topic` field (distinct from `EventType`) used by `RoutingTopic()`. The `outbox_entries` CREATE TABLE DDL omits a `topic` column. When `Topic != EventType`, the stored entry loses its routing key. On relay read, `Topic` is never scanned -- `RoutingTopic()` will silently fall back to `EventType`. |
| **F04** | `[开发者体验]` No example showing postgres adapter usage | P2 | All three examples use `eventbus.New()` (in-memory). No example demonstrates `NewPool -> NewTxManager -> NewMigrator -> NewOutboxWriter -> NewOutboxRelay` full lifecycle. |
| **F05** | `[范围偏移]` `ErrAdapterPGTxTimeout` declared but never used | P3 | Error code exists in `errors.go` but no production code references it. Either it is premature or the timeout handling is missing. |
| **F06** | `[验收标准缺失]` `ErrAdapterPGPublish` declared but never used | P3 | Same as F05 -- declared in `errors.go` but never returned anywhere in production code. |

---

## 2. API Usability (Developer Experience)

### 2.1 Creation flow

A developer wishing to use the postgres adapter would:

```go
pool, err := postgres.NewPool(ctx, postgres.ConfigFromEnv())
defer pool.Close()

txm := postgres.NewTxManager(pool)
migrator := postgres.NewMigrator(pool, postgres.MigrationsFS(), "")
writer := postgres.NewOutboxWriter()
relay := postgres.NewOutboxRelay(pool.DB(), publisher, postgres.DefaultRelayConfig())
```

**Assessment**: The creation flow is logical and discoverable. Each constructor takes minimal dependencies and produces a focused component. The `pool.DB()` bridge to `relayDB` is documented in the constructor comment.

### 2.2 Findings

| ID | Finding | Category | Severity |
|----|---------|----------|----------|
| **DX01** | `[开发者体验]` `NewOutboxRelay` accepts `relayDB` (unexported interface), but its godoc says "db is typically pool.DB()". The developer must know to call `pool.DB()` -- this works because `*pgxpool.Pool` satisfies the unexported `relayDB`, but it is not obvious from the type signature alone. | P3 | A type alias like `type DB = relayDB` or accepting `*pgxpool.Pool` directly would be clearer. |
| **DX02** | `[开发者体验]` `TxManager.RunInTx` commit failure uses `ErrAdapterPGConnect` error code (line 104 of `tx_manager.go`). A commit failure is not a "connect" problem -- it should use a dedicated code like `ErrAdapterPGQuery` or a new `ErrAdapterPGTx`. The existing unused `ErrAdapterPGTxTimeout` suggests a tx-specific code was planned. | P2 | Misleading error code confuses developers debugging commit failures. |
| **DX03** | `[开发者体验]` `OutboxRelay.pollOnce` continues publishing after one entry fails, but still commits the transaction at the end. This means entries that failed to publish will NOT be retried on the next poll because the `FOR UPDATE SKIP LOCKED` has already locked them in this transaction, and after commit the UPDATE was not applied to them. This is correct -- they remain `published=false`. However, entries that succeeded in publishing but then fail to be marked `published=true` (line 206-210) will be re-published on the next poll, causing duplicates without clear documentation of this at-least-once guarantee. | P2 | The API should document the "at-least-once delivery" semantic clearly. |
| **DX04** | `[兼容性风险]` `CtxWithTx` and `TxFromContext` are exported public APIs. Changing the context key type would break all downstream code. Current design is stable. | PASS | No issue -- observation only. |
| **DX05** | `[开发者体验]` `Migrator.latestApplied` checks `err.Error() == "no rows in result set"` (string comparison, line 275 of `migrator.go`). This is fragile -- pgx could change the error message across versions. Should use `errors.Is(err, pgx.ErrNoRows)`. | P1 | Functional correctness risk: if pgx changes the string, `Down()` will return an error instead of a no-op on empty state. |

---

## 3. Configuration

### 3.1 Pool Config

| Setting | Default | Env var | Validation |
|---------|---------|---------|------------|
| DSN | (none, required) | `GOCELL_PG_DSN` | Empty -> error |
| MaxConns | 10 | `GOCELL_PG_MAX_CONNS` | Negative -> default |
| IdleTimeout | 5m | `GOCELL_PG_IDLE_TIMEOUT` | Invalid parse -> default |
| MaxLifetime | 1h | `GOCELL_PG_MAX_LIFETIME` | Invalid parse -> default |

**Assessment**: GREEN. Defaults are sensible for a development/small-production workload. Env-based config supports 12-factor deployment. Validation silently falls back to defaults on invalid values -- this is reasonable but could surprise a developer who sets `GOCELL_PG_MAX_CONNS=abc` and expects an error.

### 3.2 RelayConfig

| Setting | Default | Env var | Configurable |
|---------|---------|---------|-------------|
| PollInterval | 1s | none | Yes (struct field) |
| BatchSize | 100 | none | Yes (struct field) |
| RetentionPeriod | 72h | none | Yes (struct field) |

**Assessment**: YELLOW. Defaults are reasonable. No env-based config option (see F02).

### 3.3 Migrator Config

| Setting | Default | Configurable |
|---------|---------|-------------|
| tableName | "schema_migrations" | Yes (constructor param) |

**Assessment**: GREEN. Customizable table name for multi-tenant scenarios.

---

## 4. Observability

### 4.1 Structured logging inventory

| Component | Event | Level | Fields |
|-----------|-------|-------|--------|
| Pool.NewPool | connected | Info | host, max_conns |
| Pool.Close | closed | Info | -- |
| TxManager.RunInTx | rollback after panic | Error | panic, rollback_error |
| TxManager.RunInTx | rollback failed | Error | original_error, rollback_error |
| TxManager.runInSavepoint | rollback savepoint after panic | Error | savepoint, panic, rollback_error |
| TxManager.runInSavepoint | rollback savepoint failed | Error | savepoint, original_error, rollback_error |
| Migrator.applyMigration | applied | Info | version, name |
| Migrator.rollbackMigration | rolled back | Info | version, name |
| Migrator.Down | no migrations to roll back | Info | -- |
| OutboxRelay.Start | started | Info | poll_interval, batch_size |
| OutboxRelay.Stop | stopped | Info | -- |
| OutboxRelay.pollLoop | poll failed | Error | error |
| OutboxRelay.pollOnce | marshal entry failed | Error | entry_id, error |
| OutboxRelay.pollOnce | publish failed | Error | entry_id, topic, error |
| OutboxRelay.pollOnce | mark published failed | Error | entry_id, error |
| OutboxRelay.cleanupLoop | cleanup failed | Error | error |
| OutboxRelay.deletePublishedBefore | cleaned up old entries | Info | deleted |
| OutboxRelay.pollOnce | metadata unmarshal failed | Warn | entry_id, error |

**Assessment**: GREEN. Comprehensive structured logging at appropriate levels. Error logs include context fields (entry_id, error). Lifecycle events at Info level.

### 4.2 Metrics

| ID | Finding | Category | Severity |
|----|---------|----------|----------|
| **O01** | `[验收标准缺失]` No Prometheus/OpenTelemetry metrics exposed | P2 | `Pool.Stats()` returns a formatted string -- useful for diagnostics but not for automated monitoring dashboards. No metrics for: connection pool utilization, query latency, outbox relay throughput, migration duration. For a framework adapter, metrics hooks (or at minimum an interface for metrics collectors) would be expected. |

---

## 5. Documentation

### 5.1 doc.go accuracy

The package doc (lines 1-16 of `doc.go`) lists four components:
- Pool -- accurate
- TxManager -- accurate
- Migrator -- accurate
- RowScanner / QueryBuilder -- accurate

**Missing from doc.go**: OutboxWriter and OutboxRelay are not mentioned. These are arguably the most important components for the L2 OutboxFact consistency level.

| ID | Finding | Category | Severity |
|----|---------|----------|----------|
| **D01** | `[开发者体验]` doc.go omits OutboxWriter and OutboxRelay from the package summary | P2 | A Go developer reading godoc will not know this package provides outbox support until they scan individual type docs. |
| **D02** | `[开发者体验]` doc.go mentions "ref: Watermill ... advisory locking" but the code does not implement advisory locking (see F01). Doc is misleading. | P2 | Either add advisory locking or correct the doc.go reference. |

### 5.2 Inline documentation quality

All exported types and functions have godoc comments. Quality is good:
- `Config` struct fields have doc comments with default values and env var names
- `RunInTx` documents nesting and panic semantics
- `OutboxWriter.Write` documents ErrAdapterPGNoTx behavior
- `OutboxRelay` comments explain the poll-publish-mark cycle

---

## 6. Cross-Adapter Consistency

### 6.1 Comparison with redis adapter

| Aspect | postgres | redis | Consistent? |
|--------|----------|-------|-------------|
| Config struct with defaults | `Config` + `applyDefaults()` | `Config` + `defaults()` | YELLOW -- method name differs (`applyDefaults` vs `defaults`) |
| Health check | `pool.Health(ctx)` | `client.Health(ctx)` | GREEN |
| Close | `pool.Close()` | `client.Close() error` | YELLOW -- postgres Close returns void; redis returns error |
| Constructor | `NewPool(ctx, cfg)` | `NewClient(ctx, cfg)` | GREEN -- same pattern |
| Error prefix | `ERR_ADAPTER_PG_*` | `ERR_ADAPTER_REDIS_*` | GREEN |
| Env-based config | `ConfigFromEnv()` | (none) | YELLOW -- inconsistent; postgres has it, redis does not |
| Stats/Diagnostics | `pool.Stats() string` | (none) | YELLOW -- postgres has it, redis does not |

### 6.2 Comparison with rabbitmq adapter

| Aspect | postgres | rabbitmq | Consistent? |
|--------|----------|----------|-------------|
| Constructor | `NewPool(ctx, cfg) (*Pool, error)` | `NewConnection(cfg, ...opts) (*Connection, error)` | YELLOW -- rabbitmq uses functional options, postgres does not. No ctx in rabbitmq constructor. |
| Health check | `pool.Health(ctx) error` | `conn.Health() error` | YELLOW -- postgres takes ctx, rabbitmq does not |
| Close | `pool.Close()` | `conn.Close() error` | Same void-vs-error gap as redis |
| Error prefix | `ERR_ADAPTER_PG_*` | `ERR_ADAPTER_AMQP_*` | GREEN |
| Reconnect/resilience | Not applicable (pgxpool handles) | Auto-reconnect with backoff | N/A |

---

## 7. Product Evaluation (7-Dimension Matrix)

| Dim | Dimension | Rating | Evidence |
|-----|-----------|--------|----------|
| A | Acceptance criteria coverage | YELLOW | Core P1 functionality (Pool, TxManager, Migrator, OutboxWriter, OutboxRelay) is implemented and tested. However: DX05 (string-based error check in Migrator) is a P1 correctness risk; F01 (missing advisory lock) contradicts documented behavior. |
| B | UI compliance (N/A for library) | GREEN | Not applicable -- this is a Go library, not a UI. Error messages are clear and actionable. |
| C | Error path coverage | GREEN | Tests cover: empty DSN, invalid DSN, unreachable host, no tx in context, exec errors, publish errors, panic recovery, rollback failures. Integration tests exercise commit/rollback/panic with real PostgreSQL. |
| D | Documentation link completeness | YELLOW | doc.go is incomplete (missing OutboxWriter/OutboxRelay). No example project demonstrates postgres adapter usage. Misleading advisory lock reference. |
| E | Feature completeness | YELLOW | 5/5 core components present. Missing: advisory lock (documented but absent), Topic column (schema gap), metrics hooks. Two error codes declared but unused. |
| F | Success criteria attainment | YELLOW | The adapter enables L2 OutboxFact workflows for downstream cells, but the missing Topic column means `outbox.Entry.Topic` is silently lost. At-least-once semantics are not documented. |
| G | Product tech debt | YELLOW | 3 items: (1) advisory lock gap, (2) Topic column gap, (3) string-based pgx error comparison. |

---

## 8. Findings Summary (sorted by severity)

| ID | Severity | Category | Summary | Suggested Fix |
|----|----------|----------|---------|---------------|
| DX05 | **P1** | `[开发者体验]` | `Migrator.latestApplied` uses `err.Error() == "no rows in result set"` string comparison instead of `errors.Is(err, pgx.ErrNoRows)` | Replace with `errors.Is` check |
| F01 | **P2** | `[验收标准缺失]` | Advisory locking documented in doc.go but not implemented in Migrator | Either implement `pg_advisory_lock` around `Up()`/`Down()` or remove the claim from doc.go |
| F03 | **P2** | `[验收标准缺失]` | `outbox_entries` DDL omits `topic` column; `outbox.Entry.Topic` is silently lost on write and relay | Add `topic TEXT NOT NULL DEFAULT ''` column; include in INSERT/SELECT |
| DX02 | **P2** | `[开发者体验]` | TxManager commit failure uses `ErrAdapterPGConnect` instead of a tx-specific error code | Use `ErrAdapterPGQuery` or introduce `ErrAdapterPGTx` |
| DX03 | **P2** | `[开发者体验]` | OutboxRelay at-least-once delivery semantics not documented | Add doc comment explaining delivery guarantee and idempotency expectations for consumers |
| D01 | **P2** | `[开发者体验]` | doc.go omits OutboxWriter and OutboxRelay | Add bullet points for both components |
| D02 | **P2** | `[开发者体验]` | doc.go advisory lock reference is inaccurate | Fix reference to match implementation |
| O01 | **P2** | `[验收标准缺失]` | No metrics (Prometheus/OTel) for pool utilization, relay throughput, migration timing | Add metrics hooks or interface, or document as future work |
| F04 | **P2** | `[开发者体验]` | No example project demonstrates postgres adapter usage | Add example or section in existing examples for "production mode with PostgreSQL" |
| F02 | **P3** | `[功能完整度]` | No env-based config for RelayConfig | Add `RelayConfigFromEnv()` or document env vars |
| F05 | **P3** | `[范围偏移]` | `ErrAdapterPGTxTimeout` declared but never used | Remove or implement tx timeout handling |
| F06 | **P3** | `[范围偏移]` | `ErrAdapterPGPublish` declared but never used | Remove or use in OutboxRelay publish error path |
| DX01 | **P3** | `[开发者体验]` | `NewOutboxRelay` takes unexported `relayDB` interface -- not obvious from API surface | Consider accepting `*pgxpool.Pool` or export the interface |

### Cross-adapter consistency items (informational)

| ID | Item | Adapters affected |
|----|------|------------------|
| CA01 | `Close()` return type inconsistency (void vs error) | postgres vs redis, rabbitmq |
| CA02 | `Health()` context parameter inconsistency | postgres vs rabbitmq |
| CA03 | Defaults method naming inconsistency (`applyDefaults` vs `defaults`) | postgres vs redis |
| CA04 | Functional options vs struct config | postgres vs rabbitmq |

---

## 9. Acceptance Criteria Verification

### P1 (Core Functionality)

| AC# | Criterion | Result | Evidence |
|-----|-----------|--------|----------|
| P1-1 | Pool connects to PostgreSQL and passes Health() | PASS | `integration_test.go:TestIntegration_Pool` -- connect_and_health, stats_non_empty, close |
| P1-2 | TxManager commits on success, rolls back on error, rolls back on panic | PASS | `integration_test.go:TestIntegration_TxManager` -- commit_path, rollback_on_error, rollback_on_panic |
| P1-3 | TxManager supports savepoint nesting | PASS | `tx_manager_test.go:TestRunInTx_NestedSavepoints` -- sp_0/sp_1 create/release verified |
| P1-4 | Migrator Up/Down/Status work correctly | PASS | `integration_test.go:TestIntegration_Migrator` -- up, up_idempotent, status, down |
| P1-5 | OutboxWriter inserts entry within transaction | PASS | `integration_test.go:TestIntegration_OutboxWriter` -- write_in_tx, write_without_tx_returns_error, write_rolled_back_in_failed_tx |
| P1-6 | OutboxRelay polls, publishes, marks entries | PASS | `outbox_relay_test.go:TestOutboxRelay_PollOnce_PublishesEntries` -- entry fetched, published, marked |
| P1-7 | Error-free handling of pgx.ErrNoRows in Migrator | **FAIL** | `migrator.go:275` uses string comparison `err.Error() == "no rows in result set"` -- see DX05 |
| P1-8 | All declared kernel interfaces satisfied at compile time | PASS | `outbox_writer.go:14`: `_ outbox.Writer = (*OutboxWriter)(nil)`; `outbox_relay.go:19-20`: `_ outbox.Relay`, `_ worker.Worker` |

### P2 (Enhanced Functionality)

| AC# | Criterion | Result | Evidence |
|-----|-----------|--------|----------|
| P2-1 | Advisory lock prevents concurrent migration | **SKIP** | Documented in doc.go but not implemented (F01). Low risk for single-pod dev but needed for production multi-pod. |
| P2-2 | Outbox Entry.Topic preserved through write/relay cycle | **SKIP** | Schema omits topic column (F03). Falls back to EventType silently. |
| P2-3 | Metrics hooks for pool and relay | **SKIP** | Not implemented (O01). Pool.Stats() provides manual diagnostics only. |
| P2-4 | Example demonstrates full postgres lifecycle | **SKIP** | No example uses postgres adapter (F04). |

### P3 (Infrastructure)

| AC# | Criterion | Result | Evidence |
|-----|-----------|--------|----------|
| P3-1 | Env-based config for RelayConfig | SKIP | Not implemented (F02) |
| P3-2 | All error codes referenced in production code | SKIP | ErrAdapterPGTxTimeout and ErrAdapterPGPublish unused (F05, F06) |

---

## 10. Product Verdict

**Status: CONDITIONAL PASS**

The postgres adapter delivers a solid, well-tested foundation for PostgreSQL integration in GoCell. Pool, TxManager, Migrator, OutboxWriter, and OutboxRelay are all functionally present and unit/integration tested. The API surface is clean, idiomatic, and generally well-documented.

### Blockers for full PASS (must fix before release)

1. **DX05 (P1)**: Replace `err.Error() == "no rows in result set"` with `errors.Is(err, pgx.ErrNoRows)` in `migrator.go:275`. This is a functional correctness issue that could break `Down()` on pgx version upgrades.

### Strongly recommended (should fix before release)

2. **F03 (P2)**: Add `topic` column to outbox DDL and include it in writer INSERT / relay SELECT. Without this, the `outbox.Entry.Topic` field is silently dropped.
3. **DX02 (P2)**: Fix error code on TxManager commit failure -- use a tx-appropriate code, not `ErrAdapterPGConnect`.
4. **D01 + D02 (P2)**: Update doc.go to (a) list OutboxWriter/OutboxRelay and (b) correct or remove the advisory lock claim.
5. **F01 (P2)**: Either implement advisory locking in Migrator or remove the claim from documentation.

### Nice to have (backlog)

6. F04: Add postgres adapter example
7. O01: Add metrics hooks
8. F02, F05, F06: Cleanup unused error codes and add env-based RelayConfig
9. CA01-CA04: Cross-adapter consistency improvements

---

## Appendix: Files Reviewed

| Absolute path | Role |
|---------------|------|
| `/Users/shengming/Documents/code/gocell/adapters/postgres/doc.go` | Package documentation |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/pool.go` | Connection pool |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/tx_manager.go` | Transaction manager |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator.go` | Migration engine |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_writer.go` | Outbox writer |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_relay.go` | Outbox relay |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/errors.go` | Error codes |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/helpers.go` | RowScanner + QueryBuilder |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/embed.go` | Embedded migrations FS |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/migrations/001_create_outbox_entries.up.sql` | Outbox DDL |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/migrations/001_create_outbox_entries.down.sql` | Outbox drop |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/pool_test.go` | Pool unit tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/tx_manager_test.go` | TxManager unit tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/migrator_test.go` | Migrator unit tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_writer_test.go` | OutboxWriter unit tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/outbox_relay_test.go` | OutboxRelay unit tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/errors_test.go` | Error code tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/helpers_test.go` | Helpers unit tests |
| `/Users/shengming/Documents/code/gocell/adapters/postgres/integration_test.go` | Integration tests (testcontainers) |
| `/Users/shengming/Documents/code/gocell/kernel/outbox/outbox.go` | Kernel interfaces |
| `/Users/shengming/Documents/code/gocell/runtime/worker/worker.go` | Worker interface |
| `/Users/shengming/Documents/code/gocell/adapters/redis/client.go` | Redis adapter (consistency comparison) |
| `/Users/shengming/Documents/code/gocell/adapters/rabbitmq/connection.go` | RabbitMQ adapter (consistency comparison) |
| `/Users/shengming/Documents/code/gocell/cells/audit-core/internal/adapters/postgres/audit_repo.go` | Downstream consumer |
| `/Users/shengming/Documents/code/gocell/cells/config-core/internal/adapters/postgres/config_repo.go` | Downstream consumer |
