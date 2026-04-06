# R1D-1 Seat 3 Review: Testing/Regression

## Scope

Reviewed `src/adapters/postgres/**` only, with emphasis on `*_test.go`, `integration_test.go`, migrations, and package-level `go test` behavior from the module root at `src/`.

## Commands Run

- `cd /Users/shengming/Documents/code/gocell/src && go test -count=1 ./adapters/postgres`
  - Fails in `TestMigrationsFS_SubDirectory`.
- `cd /Users/shengming/Documents/code/gocell/src && go test -count=1 -cover ./adapters/postgres`
  - Fails in `TestMigrationsFS_SubDirectory`; partial coverage output was `46.9%`.
- `cd /Users/shengming/Documents/code/gocell/src && go test -count=1 -tags integration ./adapters/postgres`
  - Fails in `TestIntegration_Migrator/down` and `TestMigrationsFS_SubDirectory`.

## Findings

### P1: Migration discovery unit test is pinned to a single embedded migration and is now red

`[src/adapters/postgres/migrator_test.go:216]` still asserts that `MigrationsFS()` exposes exactly one `up` migration, but the package now embeds `002_add_topic_column.up.sql` at `[src/adapters/postgres/migrations/002_add_topic_column.up.sql:1]`. The failure reproduces immediately in a clean unit run at `[src/adapters/postgres/migrator_test.go:225]`, so the package no longer has a green uncached `go test ./adapters/postgres`.

### P1: Integration rollback test still assumes a one-step migration history

`[src/adapters/postgres/integration_test.go:221]` calls `migrator.Down()` once and then expects the `outbox_entries` table to be gone and migration `001` to be unapplied at `[src/adapters/postgres/integration_test.go:231]` and `[src/adapters/postgres/integration_test.go:237]`. That assumption no longer matches the actual migrator contract in `[src/adapters/postgres/migrator.go:136]`: with `002_add_topic_column.down.sql` present at `[src/adapters/postgres/migrations/002_add_topic_column.down.sql:1]`, a single `Down()` only rolls back version `002`. The integration suite is therefore red and no longer validates multi-step rollback behavior correctly.

### P2: Relay cleanup and transaction-failure branches are still effectively untested

Critical relay paths in `[src/adapters/postgres/outbox_relay.go:184]`, `[src/adapters/postgres/outbox_relay.go:209]`, `[src/adapters/postgres/outbox_relay.go:217]`, `[src/adapters/postgres/outbox_relay.go:225]`, and `[src/adapters/postgres/outbox_relay.go:252]` are not protected by realistic tests. The local mocks in `[src/adapters/postgres/outbox_relay_test.go:171]`, `[src/adapters/postgres/outbox_relay_test.go:207]`, and `[src/adapters/postgres/outbox_relay_test.go:257]` cannot surface `Commit`, `Rollback`, or `rows.Err()` failures, and there is no package-local test that exercises `deletePublishedBefore` at all. That leaves duplicate-delivery/retry semantics and retention cleanup as unverified critical paths.

## Verdict

P0: 0

P1: 2

P2: 1

Current status: `src/adapters/postgres` is not green under clean unit or integration test runs.
