# R1D-1: adapters/postgres Six-Seat Summary

- Status: IN_PROGRESS
- Scope: `src/adapters/postgres/**`
- Method: 6 independent seats, source-only, no prior report reuse
- Model: `gpt-5.4`
- Reasoning: `xhigh`

## Seat Outputs

- Architecture: completed (`P0: 0`, `P1: 2`, `P2: 2`)
- Security: completed (`P0: 0`, `P1: 1`, `P2: 1`)
- Testing: completed (`P0: 0`, `P1: 2`, `P2: 1`)
- Ops: completed (`P0: 0`, `P1: 2`, `P2: 2`)
- DX: completed (`P0: 0`, `P1: 3`, `P2: 2`)
- Product: completed (`P0: 1`, `P1: 3`, `P2: 0`)

## Baseline Notes

- Review baseline drift detected during execution: `src/adapters/postgres/**` changed in-place while seats were running.
- Dirty files include `migrator.go`, `outbox_writer.go`, `outbox_relay.go`, related tests, and new migration `002_add_topic_column.*.sql`.
- Current package state is test-red:
  - `go test ./adapters/postgres ./kernel/outbox` fails at `src/adapters/postgres/migrator_test.go:225`
  - integration expectations around single-step `Down()` are also stale per Seat 3

## Consensus Clusters

- **High-confidence P1**: migration advisory lock handling is unsafe with `pgxpool.Pool`.
  - Reported independently by Architecture, Security, and Ops.
  - Current implementation acquires and releases `pg_advisory_lock` through `Pool.Exec`, but PostgreSQL advisory locks are session-scoped.
  - This can strand a lock on one pooled backend while the deferred unlock runs on another backend.

- **High-confidence P1**: relay retention semantics are implemented against `created_at`, not `published_at`.
  - Reported independently by Architecture, Security, Ops, DX, and Product.
  - `RelayConfig.RetentionPeriod` and `published_at` imply post-publish retention, but cleanup deletes by `created_at`.
  - Backlogged events can therefore be deleted almost immediately after finally being published.

- **High-confidence P1**: package test baseline is currently red after the migration expansion.
  - Reported by Testing and observed in local verification.
  - `TestMigrationsFS_SubDirectory` still assumes exactly one embedded migration.
  - Integration rollback expectations still assume a single `Down()` removes the whole schema.

- **Medium-confidence P2/P1**: relay lifecycle and config ergonomics remain weak.
  - Ops, DX, and Architecture all flagged some combination of:
    - no validation/defaulting for zero-value `RelayConfig`
    - `Stop` can wait indefinitely and ignores caller timeout semantics
    - `Start` returns `ctx.Err()` on graceful stop, which is a poor `worker.Worker` contract fit

## Conflicts

- **Baseline drift conflict**: Seat outputs were not produced against a frozen codebase.
  - During the run, `src/adapters/postgres/**` changed in-place, including:
    - advisory-lock additions in `migrator.go`
    - `topic` persistence changes in `outbox_writer.go` / `outbox_relay.go`
    - new migration files `002_add_topic_column.up.sql` and `002_add_topic_column.down.sql`
  - This makes cross-seat severities non-comparable unless normalized to the final on-disk state.

- **Product-seat P0 no longer stable on the latest filesystem state**:
  - Seat 6 reported a P0 that fresh migrations do not create the `topic` column.
  - On the current filesystem, `002_add_topic_column.*.sql` exists and is embedded.
  - The stable issue is not "missing topic migration" anymore; it is that tests and migration expectations were not updated after the topic-column change.

## Final Verdict

- This six-seat run completed successfully, but it is valid only as a review of the **current dirty worktree**, not of a frozen commit.
- Stable cross-seat conclusion on the latest visible code:
  - `P0`: none confirmed with high confidence on the final on-disk state
  - `P1`: migration advisory lock/session handling, published retention semantics, and red migration tests
  - `P2`: relay lifecycle/config ergonomics and remaining test gaps
- Recommended next step before any authoritative arbitration:
  - freeze the baseline, or explicitly accept the current dirty worktree as the review target
