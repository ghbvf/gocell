# R1D-1 Seat 6 Review

## Scope
- Reviewed `src/adapters/postgres/**`.
- Referenced `src/kernel/outbox/outbox.go` only where the adapter conflicts with the public outbox contract.

## Commands Run
- `rg --files src/adapters/postgres`
- `rg --files src | rg 'outbox|contract|topic|publish|retention'`
- `rg -n "RoutingTopic\\(|Topic:|RetentionPeriod|published_at|created_at <|NewOutboxWriter|NewOutboxRelay|RunInTx\\(" src`
- `rg -n "outbox\\.Entry\\s*\\{" src`
- `rg -n "func NewMigrator|migrationLockID" src/adapters/postgres/migrator.go src`
- `nl -ba src/adapters/postgres/migrator.go | sed -n '1,360p'`
- `nl -ba src/adapters/postgres/outbox_writer.go | sed -n '1,220p'`
- `nl -ba src/adapters/postgres/outbox_relay.go | sed -n '20,280p'`
- `nl -ba src/adapters/postgres/migrations/001_create_outbox_entries.up.sql | sed -n '1,120p'`
- `nl -ba src/kernel/outbox/outbox.go | sed -n '12,40p'`
- `go test ./adapters/postgres ./kernel/outbox` (run from `src/`; failed because `adapters/postgres/migrator_test.go` still expects the old `NewMigrator` signature)

## Findings

### P0 - Fresh migrations do not create the `topic` column that the runtime adapter now requires
- References: `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:1`, `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:10`, `src/adapters/postgres/outbox_writer.go:49`, `src/adapters/postgres/outbox_relay.go:148`
- The only shipped outbox migration creates `outbox_entries` without a `topic` column, but the writer now inserts `topic` and the relay now selects `topic`.
- On a clean deployment, every outbox insert and relay poll against a migrated database will fail with `column "topic" does not exist`, so the Postgres outbox pipeline is unusable end to end.

### P1 - Migration locking is session-scoped but is acquired and released through the pool, so the lock can leak or unlock the wrong session
- References: `src/adapters/postgres/migrator.go:17`, `src/adapters/postgres/migrator.go:108`, `src/adapters/postgres/migrator.go:111`, `src/adapters/postgres/migrator.go:142`, `src/adapters/postgres/migrator.go:145`
- PostgreSQL advisory locks live on a single session. `pg_advisory_lock`/`pg_advisory_unlock` are being executed through `pgxpool.Pool.Exec`, which does not pin the same connection for both calls, and the unlock result is ignored.
- A successful `Up`/`Down` can therefore leave the migration lock stranded on an idle pooled connection until that connection closes, causing later migrations to block unpredictably across replicas.

### P1 - The adapter narrows `outbox.Entry.ID` from arbitrary string to UUID without enforcing that contract at the boundary
- References: `src/kernel/outbox/outbox.go:16`, `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:2`, `src/adapters/postgres/outbox_writer.go:54`
- The public outbox contract exposes `Entry.ID` as a plain `string`, but the Postgres schema requires `UUID` and the writer forwards `entry.ID` unchanged.
- Any caller that uses a non-UUID id permitted by the contract will fail at insert time, turning event publication into a transaction failure or an event drop depending on caller error handling.

### P1 - Published retention is measured from `created_at`, not from `published_at`
- References: `src/adapters/postgres/outbox_relay.go:29`, `src/adapters/postgres/outbox_relay.go:208`, `src/adapters/postgres/outbox_relay.go:253`
- `RelayConfig.RetentionPeriod` is documented as post-publish retention, and the relay records `published_at`, but cleanup deletes rows based on `created_at`.
- Entries that waited in the outbox for a long time can be deleted almost immediately after finally being published, which violates the advertised retention semantics and removes recently published audit/debug evidence too early.

## Verdict
- P0: 1
- P1: 3
- P2: 0
