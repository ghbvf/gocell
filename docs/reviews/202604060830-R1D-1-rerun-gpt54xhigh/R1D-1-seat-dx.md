# Seat 5 Review: DX / Maintainability

## Scope
- Reviewed `src/adapters/postgres/**` only.
- Did not read any existing reports under `docs/reviews/`.

## Commands Run
- `git status --short`
- `rg --files src/adapters/postgres`
- `nl -ba src/adapters/postgres/doc.go`
- `nl -ba src/adapters/postgres/errors.go`
- `nl -ba src/adapters/postgres/helpers.go`
- `nl -ba src/adapters/postgres/pool.go`
- `nl -ba src/adapters/postgres/tx_manager.go`
- `nl -ba src/adapters/postgres/migrator.go`
- `nl -ba src/adapters/postgres/outbox_writer.go`
- `nl -ba src/adapters/postgres/outbox_relay.go`
- `nl -ba src/adapters/postgres/embed.go`
- `nl -ba src/adapters/postgres/errors_test.go`
- `nl -ba src/adapters/postgres/helpers_test.go`
- `nl -ba src/adapters/postgres/pool_test.go`
- `nl -ba src/adapters/postgres/tx_manager_test.go`
- `nl -ba src/adapters/postgres/migrator_test.go`
- `nl -ba src/adapters/postgres/outbox_writer_test.go`
- `nl -ba src/adapters/postgres/outbox_relay_test.go`
- `nl -ba src/adapters/postgres/integration_test.go`
- `rg -n "ErrAdapterPG(NoTx|TxTimeout|Publish|Marshal|Connect|Query|Migrate)" src/adapters/postgres`
- `rg -n "NewQueryBuilder|QueryBuilder|RowScanner|Stats\\(|DefaultRelayConfig|RetentionPeriod|MigrationsFS\\(" src/adapters/postgres`
- `go test ./src/adapters/postgres/...` from repo root; failed because the module root is `src/`
- `go test ./adapters/postgres` from `src/`

## Findings

### P1 - `RelayConfig` has an unsafe zero value for a public API
Refs: `src/adapters/postgres/outbox_relay.go:64`, `src/adapters/postgres/outbox_relay.go:68`, `src/adapters/postgres/outbox_relay.go:112`, `src/adapters/postgres/outbox_relay.go:226`, `src/adapters/postgres/outbox_relay.go:231`

`NewOutboxRelay` stores the caller's `RelayConfig` verbatim and never applies `DefaultRelayConfig()` or validates the fields. A zero `PollInterval` panics when `Start()` hits `time.NewTicker`, and zero `BatchSize` / `RetentionPeriod` silently create unusable behaviour. That makes the constructor easy to misuse and hard to reason about for maintainers.

### P1 - `RetentionPeriod` is documented one way and implemented another
Refs: `src/adapters/postgres/outbox_relay.go:29`, `src/adapters/postgres/outbox_relay.go:206`, `src/adapters/postgres/outbox_relay.go:251`

The config says published rows are retained for some period, and the relay records `published_at`, but cleanup deletes by `created_at`. That is a doc/code mismatch in a public API: an entry created long ago but published just now can be deleted immediately, which is surprising for anyone operating or extending the relay.

### P1 - The exported errcode taxonomy is partly dead and partly misleading
Refs: `src/adapters/postgres/errors.go:13`, `src/adapters/postgres/errors.go:26`, `src/adapters/postgres/tx_manager.go:72`, `src/adapters/postgres/tx_manager.go:104`, `src/adapters/postgres/outbox_relay.go:136`, `src/adapters/postgres/outbox_relay.go:196`, `src/adapters/postgres/outbox_relay.go:216`

`ErrAdapterPGTxTimeout` and `ErrAdapterPGPublish` are exported but unused in production code. At the same time, transaction begin/commit failures are classified as `ErrAdapterPGConnect`, which blurs connectivity failures together with transaction-lifecycle failures. For callers and operators, the package's error vocabulary is harder to trust than it looks from the exported constants.

### P2 - The package-level error-code prefix contract is already broken, and tests do not catch it
Refs: `src/adapters/postgres/doc.go:12`, `src/adapters/postgres/errors.go:20`, `src/adapters/postgres/errors.go:21`, `src/adapters/postgres/errors_test.go:10`, `src/adapters/postgres/errors_test.go:25`

`doc.go` says PostgreSQL adapter codes use the `ERR_ADAPTER_PG_*` prefix, but `ErrAdapterPGNoTx` is exported as `ERR_ADAPTER_NO_TX`. The prefix and uniqueness tests only cover four codes and omit `ErrAdapterPGNoTx`, `ErrAdapterPGMarshal`, and `ErrAdapterPGPublish`, so this drift is currently invisible to CI.

### P2 - `latestApplied` depends on matching driver error text
Refs: `src/adapters/postgres/migrator.go:273`, `src/adapters/postgres/migrator.go:275`

Handling the empty-table case via `err.Error() == "no rows in result set"` is brittle and obscures intent. A sentinel check against `pgx.ErrNoRows` would be more maintainable and would not depend on exact error wording.

## Verdict
- P0: 0
- P1: 3
- P2: 2
