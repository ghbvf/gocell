# R1D-2: adapters/redis Test Review

| Field | Value |
|---|---|
| Role | S3 Test / Regression |
| Scope | `adapters/redis/` plus direct integration usage |
| Baseline commit | `5096d4f` |
| Evidence base | Current source, tests, and local test runs only |

## Verification

- `go test ./adapters/redis` -> PASS
- `go test -cover ./adapters/redis` -> PASS, `81.4%`
- `go test ./kernel/idempotency` -> PASS
- `go test ./tests/integration -run TestOutbox -count=1` -> FAIL by design because `tests/integration` is behind the `integration` build tag

## Summary

The package has respectable default unit coverage and working happy-path tests. The missing coverage is not broad coverage; it is the exact edge behavior that defines whether the lock and idempotency primitives are trustworthy.

## Findings

### T-01 | P1 | no test covers renewal shutdown on caller context cancellation

- Files: `adapters/redis/distlock.go:117-126`, `adapters/redis/distlock_test.go:12-129`
- Evidence: all `DistLock` tests use `context.Background()` and only exercise acquire/release/conflict paths. No test cancels the acquire context and checks that the renewal loop stops.
- Risk: the package can regress on goroutine cleanup or lock release timing without any failing test.
- Recommendation: add a test that acquires with `context.WithCancel`, cancels it, waits past TTL, and verifies a second acquisition succeeds.

### T-02 | P1 | no test covers invalid TTL or expiry-driven re-acquisition

- Files: `adapters/redis/distlock.go:96-99`, `adapters/redis/distlock.go:136-139`, `adapters/redis/integration_test.go:100-125`
- Evidence: current tests never pass negative or tiny TTL values, and the integration test checks only contention while the first holder is alive.
- Risk: `time.NewTicker(ttl/2)` remains unguarded against invalid TTL inputs, and TTL-expiry behavior is unverified.
- Recommendation: add unit coverage for invalid TTL handling and integration coverage for natural expiry followed by re-acquire.

### T-03 | P2 | end-to-end integration uses manual `IsProcessed` / `MarkProcessed`, not the real atomic path

- Files: `adapters/rabbitmq/consumer_base.go:102-123`, `tests/integration/outbox_fullchain_test.go:299-333`
- Evidence: production consumer flow uses `checker.TryProcess(...)`, but the full-chain test validates only `IsProcessed` and `MarkProcessed` calls by hand.
- Risk: the package's most important integration contract, the atomic path through `TryProcess`, is not protected by an end-to-end test.
- Recommendation: extend the full-chain path or add a focused integration test that drives `ConsumerBase.Wrap()` with Redis-backed idempotency.

## Verdict

Test signoff is yellow, not green. Default unit tests are good enough to catch regressions in basic behavior, but not enough to protect the concurrency and lifecycle edge cases that actually matter for infrastructure code.
