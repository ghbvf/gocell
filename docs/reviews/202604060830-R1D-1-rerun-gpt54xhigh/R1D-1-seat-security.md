# R1D-1 Seat 2 Security Review

## Scope
- Reviewed `src/adapters/postgres/**`.
- Read direct contracts only where needed: `src/kernel/outbox/outbox.go`, `src/runtime/worker/worker.go`, and `src/pkg/errcode/errcode.go`.
- Did not read any existing reports under `docs/reviews/`.

## Commands Run
- `rg --files src/adapters/postgres`
- `rg -n "package |import |type |func " src/adapters/postgres`
- `rg -n "NewMigrator\\(|NewOutboxRelay\\(|RunInTx\\(|TxFromContext\\(" src`
- `nl -ba src/adapters/postgres/{pool.go,tx_manager.go,outbox_writer.go,outbox_relay.go,migrator.go,helpers.go}`
- `nl -ba src/adapters/postgres/migrations/{001_create_outbox_entries.up.sql,001_create_outbox_entries.down.sql,002_add_topic_column.up.sql,002_add_topic_column.down.sql}`
- `go test ./adapters/postgres -run 'Test(DefaultRelayConfig|ParseMigrationFilename|NewMigrator|Config)'` from `src/` (currently fails because concurrent `NewMigrator` signature changes have not been propagated to tests/call sites)

## Findings

### P1 - Advisory lock handling can leak the migration lock and block all future migrations
- Refs: `src/adapters/postgres/migrator.go:108`, `src/adapters/postgres/migrator.go:111`, `src/adapters/postgres/migrator.go:142`, `src/adapters/postgres/migrator.go:145`
- `pg_advisory_lock` / `pg_advisory_unlock` are session-scoped, but this code issues both through `pgxpool.Pool.Exec`, which does not pin a single backend connection. A lock acquired on one pooled session can therefore be "unlocked" on a different session, and the deferred unlock result is ignored. The original connection can keep the advisory lock until that session is recycled, causing later `Up()` / `Down()` calls to hang behind a leaked lock.

### P2 - Outbox cleanup deletes by `created_at`, collapsing the intended post-publish retention window
- Refs: `src/adapters/postgres/outbox_relay.go:29`, `src/adapters/postgres/outbox_relay.go:241`, `src/adapters/postgres/outbox_relay.go:253`, `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:8`, `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:9`
- `RelayConfig.RetentionPeriod` is documented as the time published entries are kept, and the schema records `published_at`, but cleanup deletes rows with `created_at < cutoff`. Any event that remains queued longer than the retention window becomes eligible for deletion almost immediately after it is finally published, which shortens or eliminates the replay/audit window during backlog recovery.

## Verdict
- No current SQL injection or credential leakage issue stood out in the latest on-disk code.
- Remaining risk is concentrated in migration lock handling and premature outbox cleanup.
- P0: 0
- P1: 1
- P2: 1
