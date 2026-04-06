# R1D-2 Product Manager Review: adapters/redis

## Summary

The `adapters/redis` module provides a clean, focused set of primitives (Client, Cache, DistLock, IdempotencyChecker) that serve the current GoCell consumers well. The API is generally minimal and well-documented, with sensible defaults. However, there are several DX rough edges -- notably a misleading error code constant, an ambiguous Cache.Get return value, inconsistent placement of JSON helpers as package-level generics rather than methods, a missing Delete error code, and the absence of any Redis usage in the example applications -- that would benefit from attention before the module matures further.

## Findings

### [PM-01] Cache.Get returns ("", nil) for missing keys -- ambiguous API
- **Severity**: P2
- **Category**: api-surface
- **File**: adapters/redis/cache.go:31-40
- **Description**: `Cache.Get` returns `("", nil)` when a key does not exist. This is indistinguishable from a key whose value is legitimately the empty string `""`. Most Go libraries solve this with a three-return-value signature `(string, bool, error)` where the bool indicates whether the key was found, or by returning a sentinel error (e.g. `ErrNotFound`). The current design forces callers to either assume empty strings are never stored or perform a separate existence check.
- **Impact on consumers**: Any consumer storing empty-string values or needing to distinguish "key absent" from "key present but empty" cannot rely on this API. This is a subtle correctness trap that may not surface until production.
- **Recommendation**: Change the return signature to `(string, bool, error)` where `bool` signals key existence. Alternatively, return a dedicated sentinel error such as `ErrCacheMiss`. Apply the same pattern to `GetJSON`.

### [PM-02] GetJSON / SetJSON are package-level functions, not methods on Cache
- **Severity**: P2
- **Category**: api-surface
- **File**: adapters/redis/cache.go:65, cache.go:85
- **Description**: `GetJSON[T]` and `SetJSON[T]` are top-level generic functions that accept `*Cache` as the first argument, while `Get`, `Set`, and `Delete` are methods on `Cache`. This inconsistency forces a different calling convention for JSON operations: `redis.GetJSON[MyType](ctx, cache, key)` vs `cache.Get(ctx, key)`. Go 1.21+ does not yet allow generic methods on concrete types, so the design choice is understandable, but it is not documented or explained anywhere.
- **Impact on consumers**: Discoverability suffers -- developers using IDE autocomplete on a `*Cache` value will see `Get/Set/Delete` but not `GetJSON/SetJSON`. Newcomers may not realize these functions exist.
- **Recommendation**: Add a doc comment on the `Cache` type or in `doc.go` explaining why JSON helpers are package-level functions (Go generics limitation). Consider adding non-generic convenience methods `GetJSONRaw` / `SetJSONRaw` that return `[]byte` as a bridge for discoverability.

### [PM-03] Error code constant name/value mismatch: ErrAdapterRedisLockAcquire vs "ERR_ADAPTER_REDIS_LOCK_ACQUIRED"
- **Severity**: P1
- **Category**: error-ux
- **File**: adapters/redis/client.go:16
- **Description**: The Go constant is named `ErrAdapterRedisLockAcquire` (verb: "acquire") but its string value is `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"` (past tense: "acquired"). This creates confusion: the code says "ERR...ACQUIRED" was returned, but the constant name implies the error occurred during acquisition. The mismatch is visible in logs and monitoring dashboards, making it harder for operators to correlate code paths with emitted error codes.
- **Impact on consumers**: Operators searching code for the string `"ACQUIRED"` may not find the constant `ErrAdapterRedisLockAcquire`, and vice versa. This is a naming bug that erodes trust in error code consistency.
- **Recommendation**: Align the constant value to `"ERR_ADAPTER_REDIS_LOCK_ACQUIRE"` (matching the constant name) or rename the constant to `ErrAdapterRedisLockAcquired`. Given that this code is also used for release failures and token generation failures (not just "lock acquired"), `"ERR_ADAPTER_REDIS_LOCK_ACQUIRE"` is the better fix.

### [PM-04] Lock.Release error uses ErrAdapterRedisLockAcquire -- misleading error code
- **Severity**: P2
- **Category**: error-ux
- **File**: adapters/redis/distlock.go:53-55
- **Description**: When `Lock.Release` fails, it wraps the error with `ErrAdapterRedisLockAcquire`. This code semantically means "lock acquisition failed", not "lock release failed". There is no `ErrAdapterRedisLockRelease` constant. Operators seeing `ERR_ADAPTER_REDIS_LOCK_ACQUIRED` in logs during a release operation will be misled.
- **Impact on consumers**: Incorrect error categorization in dashboards; harder incident triage.
- **Recommendation**: Introduce `ErrAdapterRedisLockRelease errcode.Code = "ERR_ADAPTER_REDIS_LOCK_RELEASE"` and use it in `Lock.Release`.

### [PM-05] Cache.Delete wraps errors with ErrAdapterRedisSet -- wrong error code
- **Severity**: P2
- **Category**: error-ux
- **File**: adapters/redis/cache.go:57
- **Description**: `Cache.Delete` uses `ErrAdapterRedisSet` for its error wrapping. A delete operation is not a set operation. There is no `ErrAdapterRedisDelete` constant.
- **Impact on consumers**: Alerts or dashboards filtering on `ERR_ADAPTER_REDIS_SET` will conflate write failures with delete failures, making it impossible to distinguish the two.
- **Recommendation**: Introduce `ErrAdapterRedisDelete errcode.Code = "ERR_ADAPTER_REDIS_DELETE"` and use it in `Cache.Delete`.

### [PM-06] Acquire returns concrete *Lock -- limits testability for consumers
- **Severity**: P3
- **Category**: api-surface
- **File**: adapters/redis/distlock.go:96
- **Description**: `DistLock.Acquire` returns `*Lock`, a concrete struct. Consumers wanting to mock distributed locking in unit tests must either use the internal `cmdable` mock (unexported) or create their own wrapper interface around `DistLock`. If `Acquire` returned a `Locker` interface (with a `Release` method), consumers could mock it trivially.
- **Impact on consumers**: `runtime/worker` or any cell using DistLock for leader election will need to define their own interface or use integration tests. This friction discourages proper unit testing.
- **Recommendation**: Define an exported `Locker` interface (`Release(ctx) error`) and have `Acquire` return it. This is a minor breaking change that can be done now while the consumer base is small.

### [PM-07] No Redis usage in any example application
- **Severity**: P2
- **Category**: documentation
- **File**: examples/sso-bff/main.go, examples/iot-device/main.go, examples/todo-order/main.go
- **Description**: All three example applications (`sso-bff`, `todo-order`, `iot-device`) use in-memory defaults and do not demonstrate `adapters/redis` usage at all. The `docker-compose.yml` files for all three examples include a Redis service, but the Go code never connects to it. There is no example showing how to create a `redis.Client`, wire `IdempotencyChecker` into a `ConsumerBase`, or use `Cache`/`DistLock` in a real application.
- **Impact on consumers**: New developers have no working reference for integrating Redis into a GoCell application. The gap between "docker-compose includes Redis" and "the code uses in-memory defaults" is confusing.
- **Recommendation**: Add Redis wiring to at least one example (e.g., `sso-bff`) behind a flag or env var, demonstrating the full `NewClient -> NewIdempotencyChecker -> ConsumerBase` path. This is also the natural place to show Sentinel configuration.

### [PM-08] No Cluster mode support -- undocumented limitation
- **Severity**: P3
- **Category**: feature-gap
- **File**: adapters/redis/client.go:26-30, adapters/redis/doc.go
- **Description**: The `Mode` enum supports only `Standalone` and `Sentinel`. Redis Cluster is not supported and this limitation is not documented anywhere. The `default` case in `NewClient`'s switch statement silently falls back to standalone mode for any unrecognized `Mode` value, including a hypothetical `"cluster"`.
- **Impact on consumers**: Teams scaling beyond Sentinel (or using managed Redis Cluster services like AWS ElastiCache Cluster Mode) will discover this gap at integration time with no prior warning.
- **Recommendation**: Document the limitation in `doc.go`. Consider adding a `ModeCluster` constant that returns an explicit "not yet supported" error rather than silently falling through to standalone.

### [PM-09] No connection pool tuning exposed
- **Severity**: P3
- **Category**: config-dx
- **File**: adapters/redis/client.go:33-63
- **Description**: The `Config` struct exposes timeouts but no connection pool settings (`PoolSize`, `MinIdleConns`, `MaxRetries`, `PoolTimeout`). The underlying `go-redis` library supports all of these. For production deployments with high concurrency, the default pool size (10 per CPU in go-redis) may be insufficient or excessive.
- **Impact on consumers**: Production operators cannot tune pool size without forking the adapter or reaching into the underlying library.
- **Recommendation**: Expose at least `PoolSize` and `MinIdleConns` in `Config` with documented defaults. This is a non-breaking addition.

### [PM-10] No metrics or instrumentation hooks
- **Severity**: P3
- **Category**: feature-gap
- **File**: adapters/redis/client.go (entire file)
- **Description**: There are no hooks for metrics collection (command latency, connection pool stats, error rates). The `go-redis` library supports `AddHook` for OpenTelemetry integration via `redisotel`, but this adapter does not expose it. Operators have no way to monitor Redis health beyond the binary `Health()` ping.
- **Impact on consumers**: Production deployments lack observability into Redis performance. Latency regressions, connection exhaustion, and slow commands will go undetected until they cause user-visible failures.
- **Recommendation**: Accept an optional `[]redis.Hook` in `Config` or provide a `WithHook` option. Document the recommended `redisotel` integration path. This can be deferred to a later milestone but should be tracked.

### [PM-11] doc.go is minimal and lacks usage examples
- **Severity**: P3
- **Category**: documentation
- **File**: adapters/redis/doc.go:1-11
- **Description**: The package doc is 11 lines and lists what the package offers, but provides no usage examples, no guidance on when to use which component, and no link to relevant architecture documentation. There are no `Example*` test functions in the test files either.
- **Impact on consumers**: Developers must read source code to understand how to wire components together (e.g., the `NewClient -> NewCache` dependency chain).
- **Recommendation**: Add a `doc.go` example showing the typical wiring: `NewClient(ctx, cfg) -> NewCache(client) -> cache.Set(...)`. Add at least one `Example` test function for the most common use case (IdempotencyChecker, since it is the primary consumer-facing feature).

### [PM-12] Health check in Sentinel mode reports standalone addr in error message
- **Severity**: P3
- **Category**: error-ux
- **File**: adapters/redis/client.go:162-165
- **Description**: The `Health()` method formats the error message as `redis: health check failed (addr=%s)` using `c.config.Addr`. In Sentinel mode, `Addr` is empty (the user provides `SentinelAddrs` instead), so the error message will read `redis: health check failed (addr=)`, providing no useful diagnostic information.
- **Impact on consumers**: Operators running Sentinel mode get unhelpful error messages during connection failures, the most critical time to have good diagnostics.
- **Recommendation**: Conditionally include `SentinelAddrs` and `SentinelMaster` in the error message when `Mode == ModeSentinel`.

### [PM-13] NewClient logs addr even in Sentinel mode
- **Severity**: P3
- **Category**: error-ux
- **File**: adapters/redis/client.go:146-148
- **Description**: The success log `redis: connected` always logs `"addr", cfg.Addr`. In Sentinel mode, `Addr` is empty. The log should include sentinel addresses and master name for Sentinel deployments.
- **Impact on consumers**: Operators see `addr=""` in startup logs for Sentinel deployments, reducing log utility.
- **Recommendation**: Log mode-appropriate connection details: `addr` for standalone, `sentinelAddrs` + `sentinelMaster` for sentinel.

### [PM-14] DistLock.Acquire does not support waiting/retry with timeout
- **Severity**: P3
- **Category**: feature-gap
- **File**: adapters/redis/distlock.go:96
- **Description**: `Acquire` performs a single `SET NX` attempt and returns immediately if the lock is held. There is no option to wait/retry for a configurable duration. The doc comment references `ErrAdapterRedisLockTimeout` (implying a timeout concept), but the behavior is instant failure. For leader election scenarios, consumers must implement their own retry loop.
- **Impact on consumers**: `runtime/worker` or similar consumers performing leader election will need to build retry/backoff logic on top of `Acquire`, duplicating effort across consumers.
- **Recommendation**: Consider adding an `AcquireWithRetry(ctx, key, ttl, retryInterval, maxWait)` method or accepting options. This can be deferred but should be documented as a known gap.

## Verdict

**PASS_WITH_CONDITIONS**

Conditions for unconditional PASS:
1. **Must fix (P1)**: PM-03 -- Align the `ErrAdapterRedisLockAcquire` constant value with its name to eliminate the naming mismatch.
2. **Should fix (P2)**: PM-04 and PM-05 -- Introduce correct error codes for Lock.Release and Cache.Delete so operators can distinguish failure modes.
3. **Should fix (P2)**: PM-01 -- Address the ambiguous `Cache.Get` return value for missing keys, either with a three-value return or a sentinel error.
4. **Should address (P2)**: PM-07 -- Add Redis wiring to at least one example application to close the documentation gap.

The remaining P3 items (PM-06, PM-08, PM-09, PM-10, PM-11, PM-12, PM-13, PM-14) are tracked recommendations that can be addressed in subsequent iterations without blocking current consumers.
