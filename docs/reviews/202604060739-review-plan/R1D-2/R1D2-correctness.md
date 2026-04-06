# R1D-2 Correctness Review: adapters/redis

## Summary

The `adapters/redis` module is structurally sound and uses correct Redis patterns (SetNX for locks, Lua scripts for atomic release/renew). However, there are several correctness issues: a confirmed goroutine leak in `renewLoop`, a missing error code for Delete operations, ambiguous nil-return semantics in Cache.Get/GetJSON, a silent no-op in `MarkProcessed`, and an error code naming inconsistency. Test coverage is adequate for happy paths but lacks concurrency, TTL-expiration, and renewLoop lifecycle tests.

## Findings

### [CR-01] renewLoop goroutine leak when Release is never called
- **Severity**: P1
- **Category**: lifecycle
- **File**: `adapters/redis/distlock.go:117`
- **Description**: `Acquire()` spawns a `renewLoop` goroutine with `context.WithCancel(context.Background())`. The only way to stop this goroutine is via `Lock.Release()`, which calls `cancel()`. If the caller never calls `Release()` (e.g., due to a panic, forgotten defer, or process-level error recovery that skips the defer), the goroutine runs indefinitely. It will only terminate when `renewLoop` encounters a Redis error or detects lock loss -- but if the lock keeps being renewed successfully, the goroutine persists for the lifetime of the process. Using `context.Background()` as the parent means the caller's context cancellation (e.g., request timeout) does NOT stop renewal.
- **Impact**: Goroutine leak. In a long-running service with repeated lock acquisitions where Release is occasionally missed, goroutines accumulate, each holding a ticker and periodically hitting Redis.
- **Recommendation**: Parent the renewCtx from the caller's ctx passed to `Acquire()`, so that request/operation cancellation also stops renewal. The renew context should be `context.WithCancel(ctx)` instead of `context.WithCancel(context.Background())`. Additionally, consider adding a maximum lifetime to the renewal loop (e.g., stop after N*TTL).

### [CR-02] renewLoop silently stops on first Eval error -- lock drifts to expiry
- **Severity**: P2
- **Category**: error-handling
- **File**: `adapters/redis/distlock.go:148-154`
- **Description**: When `Eval` fails in `renewLoop` (e.g., transient network blip), the goroutine logs the error and returns immediately. No retry is attempted. The lock holder continues operating in the critical section, unaware that renewal has stopped. The lock will expire after the original TTL, and another process may acquire it, leading to split-brain in the critical section.
- **Impact**: A single transient Redis error during renewal silently downgrades the lock to a fixed-TTL lock. If the critical section runs longer than the remaining TTL, two holders can operate concurrently.
- **Recommendation**: (1) Retry transient errors with backoff before giving up. (2) Provide a notification channel or callback on the `Lock` struct so callers can detect when renewal fails. (3) Consider a "fencing token" pattern for callers that need strict mutual exclusion.

### [CR-03] Release uses `ErrAdapterRedisLockAcquire` error code instead of a dedicated release error
- **Severity**: P2
- **Category**: error-handling
- **File**: `adapters/redis/distlock.go:54`
- **Description**: `Lock.Release()` wraps Eval errors with `ErrAdapterRedisLockAcquire` (code string `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"`). This is semantically wrong -- a release failure is not an acquire failure. Callers monitoring error codes will not be able to distinguish acquire failures from release failures.
- **Impact**: Misleading error classification in observability/alerting. Operators seeing `ERR_ADAPTER_REDIS_LOCK_ACQUIRED` cannot tell if the issue was during lock acquisition or release.
- **Recommendation**: Add a dedicated `ErrAdapterRedisLockRelease` error code and use it in `Release()`.

### [CR-04] Error code typo: `ErrAdapterRedisLockAcquire` maps to `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"` (past tense)
- **Severity**: P2
- **Category**: logic-bug
- **File**: `adapters/redis/client.go:16`
- **Description**: The Go constant is named `ErrAdapterRedisLockAcquire` (present tense, imperative), but its string value is `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"` (past tense, suggesting the lock WAS acquired rather than that acquisition FAILED). This creates confusion: the error code is used when acquisition fails, yet its value says "ACQUIRED".
- **Impact**: Confusion in log analysis and error handling. The code string suggests success ("lock acquired") while it is actually used to indicate failure.
- **Recommendation**: Change the string value to `"ERR_ADAPTER_REDIS_LOCK_ACQUIRE"` to match the constant name and semantics.

### [CR-05] Cache.Delete uses `ErrAdapterRedisSet` error code
- **Severity**: P2
- **Category**: error-handling
- **File**: `adapters/redis/cache.go:57`
- **Description**: `Cache.Delete()` wraps errors with `ErrAdapterRedisSet` (code `"ERR_ADAPTER_REDIS_SET"`). A delete failure is not a set failure. The error message text says "cache delete failed" but the error code says SET.
- **Impact**: Misleading error classification. Alerts or dashboards filtering on `ERR_ADAPTER_REDIS_SET` will conflate set and delete failures.
- **Recommendation**: Add a dedicated `ErrAdapterRedisDelete` error code (`"ERR_ADAPTER_REDIS_DELETE"`).

### [CR-06] Cache.Get and GetJSON return zero-value + nil for missing keys -- callers cannot distinguish "not found" from "empty value"
- **Severity**: P2
- **Category**: logic-bug
- **File**: `adapters/redis/cache.go:31-40` and `adapters/redis/cache.go:65-81`
- **Description**: `Cache.Get` returns `("", nil)` for both "key does not exist" and "key exists with empty string value". `GetJSON[T]` returns `(zero, nil)` for both "key does not exist" and "key exists with JSON-encoded zero value" (e.g., `0`, `""`, `false`, `null`). There is no sentinel error, boolean flag, or wrapper type to distinguish these cases.
- **Impact**: Callers that need to differentiate a cache miss from a stored zero/empty value cannot do so. For example, caching a boolean `false` or integer `0` becomes indistinguishable from a cache miss. This is a common "stampede" bug: the caller treats a valid cached zero as a miss and re-fetches from the backend.
- **Recommendation**: Either (a) return a `(value, found bool, err error)` triple, (b) return a sentinel error like `ErrCacheMiss`, or (c) return a wrapper type `CacheResult[T]` with a `Found` field. The current doc comment says "returns zero + nil for missing", which is technically correct documentation, but the API design itself is ambiguous.

### [CR-07] MarkProcessed silently succeeds when key already exists
- **Severity**: P2
- **Category**: logic-bug
- **File**: `adapters/redis/idempotency.go:52-58`
- **Description**: `MarkProcessed` uses `SetNX`, which returns `false` when the key already exists. The return value is discarded (`_, err := ...`), and the method returns `nil`. The caller has no way to know whether the key was freshly marked or was already present. The doc comment says "the operation is a no-op and returns nil", which is intentional -- but this means the two-step pattern `IsProcessed` + `MarkProcessed` has a TOCTOU race: between the check and the mark, another process could have marked the key. The caller of `MarkProcessed` then silently fails to mark but thinks it succeeded.
- **Impact**: In the two-step pattern, two concurrent consumers could both see `IsProcessed=false`, both call `MarkProcessed`, and both proceed to process. Only `TryProcess` is truly safe. However, since the `Checker` interface exposes both `IsProcessed/MarkProcessed` AND `TryProcess`, callers may use the unsafe pattern.
- **Recommendation**: (1) Consider returning `(bool, error)` from `MarkProcessed` to indicate whether the mark was actually written. (2) Add a doc comment warning callers to prefer `TryProcess` for atomic check-and-mark. (3) Alternatively, deprecate the two-step methods if `TryProcess` is the canonical path.

### [CR-08] TTL=0 in MarkProcessed and TryProcess creates permanent idempotency keys
- **Severity**: P1
- **Category**: logic-bug
- **File**: `adapters/redis/idempotency.go:52` and `adapters/redis/idempotency.go:65`
- **Description**: Both `MarkProcessed` and `TryProcess` pass the TTL directly to `SetNX`. When `ttl=0`, Redis interprets this as "no expiration", creating a key that persists forever. There is no validation or default. The `Checker` interface does not enforce a minimum TTL either.
- **Impact**: If a caller passes `ttl=0` (accidentally or due to a misconfigured default), idempotency keys accumulate permanently in Redis, causing unbounded memory growth. Over time this degrades Redis performance and can lead to OOM.
- **Recommendation**: (1) Validate that TTL > 0 and return an error if zero. (2) Or apply a sensible default (e.g., 24h) when TTL is zero. (3) Document the TTL requirement prominently.

### [CR-09] Lock.cancel field not guarded -- double Release from concurrent goroutines
- **Severity**: P2
- **Category**: race-condition
- **File**: `adapters/redis/distlock.go:46-61`
- **Description**: `Lock.Release()` reads and calls `l.cancel` without synchronization. If two goroutines call `Release()` concurrently, both may call `l.cancel()` (calling a CancelFunc twice is safe per context docs), but both will also issue the Eval script concurrently. While the Lua script itself is atomic on the Redis side (one will DEL, one will get 0), the Go-side `cancel()` call races with the renewLoop goroutine's context check. This is unlikely to cause data corruption but could trigger the Go race detector.
- **Impact**: Low practical risk (the Lua script ensures correctness on the Redis side), but a race detector violation in tests would be a CI failure. Also, calling `cancel` after the `Release` Eval would be slightly more correct (stop renewal only after confirming release).
- **Recommendation**: (1) Use `sync.Once` to ensure `Release` logic executes only once. (2) Or set `l.cancel = nil` after calling it (still needs a mutex for concurrent access). (3) Alternatively, document that `Release` must not be called concurrently.

### [CR-10] renewLoop does not renew immediately -- first renewal is after TTL/2
- **Severity**: P2
- **Category**: logic-bug
- **File**: `adapters/redis/distlock.go:136-165`
- **Description**: The `renewLoop` creates a ticker at `ttl/2` interval. The first tick fires after `ttl/2`. If the lock was acquired at T=0 with TTL=30s, the first renewal happens at T=15s, extending the TTL to T=45s. This is correct behavior for steady state. However, if the Acquire itself takes significant time (e.g., due to network latency) and the caller's work starts late, the effective safety margin is reduced. More importantly, if `ttl/2` is calculated from a very short TTL (e.g., 1s -> 500ms interval), clock jitter could cause the first renewal to fire after the lock has already expired.
- **Impact**: With very short TTLs (under ~2 seconds), there is a risk that the renewal fires too late and the lock expires before the first renewal. This is an edge case but could manifest in tests with aggressive TTLs.
- **Recommendation**: Document a minimum recommended TTL (e.g., 5s). Or renew at TTL/3 for more headroom.

### [CR-11] No tests for renewLoop goroutine behavior
- **Severity**: P2
- **Category**: test-gap
- **File**: `adapters/redis/distlock_test.go`
- **Description**: The test suite does not verify that the renewLoop goroutine (a) actually renews the lock, (b) stops when Release is called, or (c) stops when the Eval call fails. The mock supports Eval for both release and renew scripts, so such tests are feasible. The renewLoop is a critical component for lock safety, and its complete absence from tests is a gap.
- **Impact**: Regressions in renewal logic (e.g., wrong interval, wrong script, wrong args) would go undetected.
- **Recommendation**: Add tests that: (1) acquire a lock with a short TTL, sleep past the original TTL, and verify the key still exists (proving renewal works); (2) acquire, release, and verify the renewLoop goroutine has exited (e.g., via runtime.NumGoroutine or channel signal); (3) inject an evalErr mid-renewal and verify the goroutine exits.

### [CR-12] No TTL expiration tests in unit tests
- **Severity**: P2
- **Category**: test-gap
- **File**: `adapters/redis/cache_test.go`, `adapters/redis/idempotency_test.go`
- **Description**: The mock correctly simulates TTL expiration (checking `time.Now().After(entry.expiry)`), but no unit test exercises this path. No test sets a short TTL, sleeps, and verifies the key has expired. The integration test `TestIntegration_IdempotencyKeyExpiry` also does not test actual expiration (it only tests mark + check, not waiting for expiry).
- **Impact**: TTL expiration behavior is untested. If the mock's expiration logic were broken, or if production code mishandled expired keys, no test would catch it.
- **Recommendation**: Add a unit test with a very short TTL (e.g., 1ms), a brief sleep (e.g., 5ms), and verify Get returns empty / IsProcessed returns false.

### [CR-13] No concurrent access tests
- **Severity**: P2
- **Category**: test-gap
- **File**: all test files
- **Description**: No test exercises concurrent access patterns: concurrent Cache.Get/Set, concurrent DistLock.Acquire on the same key, concurrent IdempotencyChecker.TryProcess on the same key, or concurrent Release. While the mock uses a `sync.Mutex`, no test verifies that the production code + mock are safe under concurrent goroutines (e.g., via `t.Parallel()` or explicit goroutine fan-out).
- **Impact**: Race conditions in either the production code or the mock would go undetected. The Go race detector only finds races in code that is actually exercised concurrently.
- **Recommendation**: Add tests that launch multiple goroutines contending on the same key for each component. Run with `-race` to verify.

### [CR-14] Client concurrency safety relies on immutable rdb field -- acceptable but undocumented
- **Severity**: P3
- **Category**: lifecycle
- **File**: `adapters/redis/client.go:101-104`
- **Description**: `Client.rdb` is set once in the constructor and never mutated. All methods (`Health`, `Close`, `cmdable`) read this field without synchronization. This is safe because the field is effectively immutable after construction. However, calling `Close()` concurrently with other methods could lead to errors (Close shuts down the underlying connection pool while Health/Get/Set are in-flight). The `go-redis` client handles this gracefully (returning pool-closed errors), so this is acceptable but undocumented.
- **Impact**: Minimal. The underlying go-redis library is documented as safe for concurrent use.
- **Recommendation**: Add a brief doc comment on `Client` noting it is safe for concurrent use (since the underlying go-redis client is).

## Cross-Reference Verification

| Finding ID | Status | Evidence |
|-----------|--------|----------|
| P1-J7 | CONFIRMED | `distlock.go:117` uses `context.WithCancel(context.Background())`. The renewLoop goroutine is only stopped by `Release()` calling `cancel()`. If Release is never called, the goroutine leaks. See CR-01 above. |
| P1-L7 | CONFIRMED | No unit or integration test verifies TTL expiration. The mock supports it (`mock_test.go:88-89` checks `time.Now().After(entry.expiry)`), but no test exercises the path. See CR-12 above. The integration test `TestIntegration_IdempotencyKeyExpiry` only tests mark+check, not waiting for expiry. |
| P1-K10 | CONFIRMED | `idempotency.go:53` passes `ttl` directly to `SetNX`. When `ttl=0`, Redis `SET NX` with expiration=0 creates a key with no expiry (permanent). No validation exists. See CR-08 above. |

## Verdict

**PASS_WITH_CONDITIONS**

Conditions for passing:
1. **Must fix (P1)**: CR-01 (renewLoop goroutine leak) -- parent renewCtx from caller's context.
2. **Must fix (P1)**: CR-08 (TTL=0 permanent keys) -- validate TTL > 0 or apply default.
3. **Should fix before merge (P2)**: CR-04 (error code typo), CR-05 (Delete error code), CR-03 (Release error code).
4. **Should add tests (P2)**: CR-11 (renewLoop tests), CR-12 (TTL expiration tests), CR-13 (concurrency tests).
5. **Design consideration (P2)**: CR-06 (cache miss ambiguity) and CR-07 (MarkProcessed silent success) are API design issues that should be tracked and addressed before the API is considered stable.
