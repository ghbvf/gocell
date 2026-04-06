# R1D-1 Seat 1 (Architecture)

## Scope
- Reviewed `src/adapters/postgres/**`.
- Read direct contracts only where needed: `src/kernel/outbox/outbox.go` and `src/runtime/worker/worker.go`.
- Did not read any existing review reports under `docs/reviews/`.

## Commands run
- `rg --files src/adapters/postgres src/kernel/outbox src/runtime/worker`
- `nl -ba src/adapters/postgres/{doc.go,tx_manager.go,migrator.go,outbox_writer.go,outbox_relay.go,pool.go,errors.go,helpers.go,embed.go}`
- `nl -ba src/adapters/postgres/migrations/{001_create_outbox_entries.up.sql,001_create_outbox_entries.down.sql,002_add_topic_column.up.sql,002_add_topic_column.down.sql}`
- `nl -ba src/kernel/outbox/outbox.go`
- `nl -ba src/runtime/worker/{worker.go,worker_test.go,periodic.go,doc.go}`
- `rg -n "NewOutboxRelay|NewOutboxWriter|NewTxManager|RunInTx\\(|TxFromContext\\(|CtxWithTx\\(|MigrationsFS\\(|NewMigrator\\(" src`
- `rg -n "advisory|pg_advisory|LOCK TABLE|topic" src/adapters/postgres`
- `rg -n "migrationLockID|pg_try_advisory|pg_advisory|validateIdentifier|NewMigrator\\(" src/adapters/postgres`
- `go test ./adapters/postgres ./kernel/outbox ./runtime/worker` (from `/Users/shengming/Documents/code/gocell/src`; currently fails in `TestMigrationsFS_SubDirectory`)

## Findings
1. **P1** The new advisory-lock path is not session-safe with `pgxpool.Pool`. `Up` and `Down` acquire and release `pg_advisory_lock` through `m.pool.Exec(...)` at `src/adapters/postgres/migrator.go:107-111` and `src/adapters/postgres/migrator.go:141-145`, but PostgreSQL advisory locks are session-scoped. Because `pgxpool.Pool.Exec` does not pin a backend session, the deferred unlock can run on a different connection than the one that acquired the lock, leaving the original lock held in the pool and making future migration attempts block unpredictably.
2. **P1** Relay retention is measured from creation time instead of publication time. `RelayConfig.RetentionPeriod` is documented as post-publication retention at `src/adapters/postgres/outbox_relay.go:29-30`, and the schema has a dedicated `published_at` column at `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:8-10`, but cleanup deletes on `created_at` at `src/adapters/postgres/outbox_relay.go:251-253`. Under backlog or broker outage, a freshly published old row can be deleted immediately, collapsing the intended replay/audit window.
3. **P2** `OutboxRelay` does not satisfy the `worker.Worker` shutdown contract cleanly. `worker.Worker` says non-nil `Start` errors signal abnormal exit at `src/runtime/worker/worker.go:17-22`, but `OutboxRelay.Start` always returns `ctx.Err()` after `Stop` cancels its context at `src/adapters/postgres/outbox_relay.go:74-96`. A graceful relay stop is therefore surfaced as `context.Canceled`/`context.DeadlineExceeded` instead of a clean exit when consumed as a worker.
4. **P2** The adapter schema narrows the kernel outbox contract by requiring UUID ids without documenting or validating that requirement at the boundary. `outbox.Entry.ID` is only a string contract at `src/kernel/outbox/outbox.go:14-16`, but the table schema fixes `id` to `UUID` at `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:1-3`, and `OutboxWriter` forwards `entry.ID` directly at `src/adapters/postgres/outbox_writer.go:53-61`. Non-UUID ids fail late at the database layer instead of at the adapter boundary.

## Verdict
- P0: 0
- P1: 2
- P2: 2
