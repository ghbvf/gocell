# R1D-1 Seat 4 Ops/Reliability Review

## Scope
- Reviewed `src/adapters/postgres/**` only.
- Focused on pool config, health checks, shutdown, logging/observability, cleanup/retention, startup multi-instance behavior, and failure handling under deploy/runtime conditions.
- Did not inspect any existing reports under `docs/reviews/`.

## Commands Run
- `rg --files src/adapters/postgres`
- `git status --short`
- `rg -n "MaxConns|MinConns|Ping|Close\\(|logger|log|health|cleanup|retention|shutdown|context|Listen|Notify|pool|Acquire|Release|migrate|lock|leader|retry|backoff|ticker|signal" src/adapters/postgres`
- `sed -n '1,220p' src/adapters/postgres/pool.go`
- `sed -n '1,260p' src/adapters/postgres/migrator.go`
- `sed -n '260,420p' src/adapters/postgres/migrator.go`
- `sed -n '1,320p' src/adapters/postgres/outbox_relay.go`
- `sed -n '1,260p' src/adapters/postgres/tx_manager.go`
- `sed -n '1,220p' src/adapters/postgres/outbox_writer.go`
- `sed -n '1,200p' src/adapters/postgres/errors.go`
- `nl -ba src/adapters/postgres/pool.go | sed -n '1,220p'`
- `nl -ba src/adapters/postgres/migrator.go | sed -n '1,260p'`
- `nl -ba src/adapters/postgres/migrator.go | sed -n '260,420p'`
- `nl -ba src/adapters/postgres/outbox_relay.go | sed -n '1,320p'`
- `nl -ba src/adapters/postgres/tx_manager.go | sed -n '1,260p'`
- `nl -ba src/adapters/postgres/migrations/001_create_outbox_entries.up.sql | sed -n '1,220p'`

## Findings
1. **P1 - Migrator does not implement the advertised startup lock, so concurrent deploys can race each other during `Up()`.** Refs: `src/adapters/postgres/migrator.go:38-40`, `src/adapters/postgres/migrator.go:80-104`, `src/adapters/postgres/migrator.go:283-315`.
   The type comment says advisory locking prevents concurrent execution, but `Up()` only reads the applied set and then runs each migration in its own transaction. Two instances starting together can both decide the same version is pending, both execute the SQL, and then one loses on the tracking-table insert or on non-idempotent DDL. That turns a normal rolling deploy into a startup failure path.

2. **P1 - Retention is enforced against `created_at`, not `published_at`, so long-queued events can be deleted immediately after they are finally delivered.** Refs: `src/adapters/postgres/outbox_relay.go:29-30`, `src/adapters/postgres/outbox_relay.go:239-252`, `src/adapters/postgres/migrations/001_create_outbox_entries.up.sql:8-10`.
   `RetentionPeriod` is documented as how long published entries are kept, but cleanup calculates the cutoff from wall-clock time and deletes `published = true AND created_at < $1`. An event created days ago and published just now can disappear on the next cleanup tick instead of being retained for the configured post-publish window, which breaks audit/replay expectations during incident response.

3. **P2 - Relay config is never defaulted or validated, making bad deploy-time settings crash the process or silently disable useful behavior.** Refs: `src/adapters/postgres/outbox_relay.go:33-39`, `src/adapters/postgres/outbox_relay.go:64-69`, `src/adapters/postgres/outbox_relay.go:112`, `src/adapters/postgres/outbox_relay.go:154`, `src/adapters/postgres/outbox_relay.go:226-239`.
   `DefaultRelayConfig()` exists, but `NewOutboxRelay()` accepts raw values without guardrails. `PollInterval <= 0` will panic in `time.NewTicker`, `BatchSize <= 0` makes polling ineffective, and `RetentionPeriod <= 0` makes cleanup eligible to delete everything already published. `pool.go` explicitly normalizes invalid config; the relay does not.

4. **P2 - `Stop` ignores its timeout context and can hang shutdown indefinitely.** Refs: `src/adapters/postgres/outbox_relay.go:98-107`, `src/adapters/postgres/outbox_relay.go:196-215`, `src/adapters/postgres/outbox_relay.go:250-255`.
   `Stop(_ context.Context)` discards the caller's deadline and waits unbounded on `wg.Wait()`. If a publish or database call blocks in a way that does not unwind promptly on cancellation, the relay can stall pod termination or rolling restarts with no timeout escape hatch.

## Verdict
- P0: 0
- P1: 2
- P2: 2
