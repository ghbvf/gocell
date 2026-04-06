# R1D-2 Architect Review: adapters/redis

## Summary

The `adapters/redis` module provides a clean, well-structured adapter with proper dependency isolation. It correctly implements the `kernel/idempotency.Checker` interface and exposes Cache, DistLock, and IdempotencyChecker as composable components built on top of a shared `cmdable` abstraction. However, there are significant gaps: no kernel-level interface definitions for Cache and DistLock (forcing consumers to depend on the concrete adapter), a goroutine lifecycle issue in the lock renewal loop, missing connection pool tuning, no fencing token support, and no path to Redis Cluster mode.

## Findings

### [A-01] Cache and DistLock lack kernel-level interface definitions
- **Severity**: P1
- **Category**: interface
- **File**: `adapters/redis/cache.go:15`, `adapters/redis/distlock.go:65`
- **Description**: `IdempotencyChecker` correctly implements `kernel/idempotency.Checker`, demonstrating the project's interface-driven pattern. However, `Cache` and `DistLock` are concrete types with no corresponding interfaces in `kernel/` or `runtime/`. Any consumer (e.g., a Cell or runtime component) that needs caching or distributed locking must import `adapters/redis` directly, violating the dependency rule that `cells/` must not depend on `adapters/`. A search of `kernel/` and `runtime/` for `Cache`, `Cacher`, `Locker`, or `DistLock` interfaces returned zero results.
- **Recommendation**: Define `kernel/cache.Store` (with `Get`, `Set`, `Delete` methods) and `kernel/distlock.Locker` (with `Acquire` returning a `Lock` interface with `Release`). Add compile-time interface checks in the adapter, mirroring the `idempotency.go` pattern (`var _ cache.Store = (*Cache)(nil)`).

### [A-02] `cmdable` interface leaks go-redis return types
- **Severity**: P2
- **Category**: abstraction
- **File**: `adapters/redis/client.go:89-97`
- **Description**: The `cmdable` interface returns go-redis specific types (`*goredis.StatusCmd`, `*goredis.StringCmd`, `*goredis.BoolCmd`, `*goredis.IntCmd`, `*goredis.Cmd`). While this is internal and unexported, it means the entire test mock (`mock_test.go`) must construct go-redis command objects, and any alternative backend (e.g., an in-memory implementation for integration testing) must also depend on `go-redis/v9`. This is acceptable for an adapter-internal seam but should be documented as intentional to prevent future confusion.
- **Recommendation**: Add a doc comment to `cmdable` stating it is intentionally coupled to go-redis types as an internal testing seam, not a general abstraction boundary. The real abstraction boundary should be the kernel-level interfaces from A-01.

### [A-03] `renewLoop` uses `context.Background()` -- goroutine lifecycle risk
- **Severity**: P1
- **Category**: architecture
- **File**: `adapters/redis/distlock.go:117`
- **Description**: `Acquire` creates a renewal context via `context.WithCancel(context.Background())`. This means the renewal goroutine is completely disconnected from the caller's context. If the caller's context is cancelled (e.g., server shutdown, request timeout), the renewal goroutine continues running indefinitely until `Release` is explicitly called. If `Release` is never called (e.g., panic, process crash without deferred cleanup), the goroutine leaks. The goroutine also performs Redis network calls that will never respect the caller's cancellation.
- **Recommendation**: Derive the renewal context from the caller's context: `renewCtx, cancel := context.WithCancel(ctx)`. This ensures that if the parent context is cancelled, the renewal loop also stops. The `Lock.Release` method already calls `cancel()`, so explicit release still works. Additionally, consider a maximum lifetime bound (e.g., `context.WithTimeout`) as a safety net.

### [A-04] No connection pool configuration
- **Severity**: P1
- **Category**: architecture
- **File**: `adapters/redis/client.go:33-63`
- **Description**: `Config` exposes dial/read/write timeouts but no connection pool parameters. The go-redis `Options` struct supports `PoolSize`, `MinIdleConns`, `MaxIdleConns`, `PoolTimeout`, `MaxRetries`, and `MaxRetryBackoff`. The current code uses go-redis defaults (10 connections per CPU). In production under load, the default pool may be too small or too large, and there is no way to tune it without modifying source code.
- **Recommendation**: Add pool-related fields to `Config` (`PoolSize`, `MinIdleConns`, `PoolTimeout`) and wire them into both `goredis.Options` and `goredis.FailoverOptions`. Apply sensible defaults (e.g., `PoolSize` = 0 means "use go-redis default").

### [A-05] No Redis Cluster mode support
- **Severity**: P2
- **Category**: architecture
- **File**: `adapters/redis/client.go:25-30`, `client.go:112-132`
- **Description**: `Mode` only supports `ModeStandalone` and `ModeSentinel`. Redis Cluster is a common production topology, especially for horizontal scaling. Adding it later requires modifying the `Mode` enum, `Config` (to add `ClusterAddrs`), and the `NewClient` switch. The `cmdable` interface is compatible with `goredis.ClusterClient` since it uses the same `Cmdable` surface, so the abstraction is ready. However, `DistLock` with a single SET NX is not safe across multiple Redis masters in a cluster without Redlock -- this architectural constraint should be documented.
- **Recommendation**: (1) Add `ModeCluster` to the `Mode` enum and a `ClusterAddrs []string` field to `Config`. (2) Add a `case ModeCluster` in `NewClient` using `goredis.NewClusterClient`. (3) Document that `DistLock` provides single-instance locking semantics and is NOT equivalent to Redlock for multi-master deployments.

### [A-06] DistLock has no fencing token -- unsafe for protecting external state
- **Severity**: P0
- **Category**: architecture
- **File**: `adapters/redis/distlock.go:37-62`, `distlock.go:96-133`
- **Description**: The `Lock` struct contains a `value` field (random token) used for ownership verification on release and renewal. However, this token is not exposed to the caller and cannot be used as a fencing token. Without fencing, the following race is possible: (1) Client A acquires lock, (2) Client A's lock expires (GC pause, network delay), (3) Client B acquires lock, (4) Client A resumes and writes to the protected resource, believing it still holds the lock. The renewal loop mitigates but does not eliminate this -- a sufficiently long GC pause between the last successful renewal and the write can still cause the race.
- **Recommendation**: (1) Expose the lock token via `Lock.Token() string` so callers can pass it as a fencing token to downstream systems that support it. (2) Add a `Lock.Valid(ctx context.Context) (bool, error)` method that checks if the lock is still held (GET + compare). (3) Document in the package doc that DistLock provides best-effort mutual exclusion and that callers protecting external state MUST use the fencing token where possible.

### [A-07] Error code value has naming inconsistency
- **Severity**: P2
- **Category**: interface
- **File**: `adapters/redis/client.go:16`
- **Description**: The Go constant is named `ErrAdapterRedisLockAcquire` (verb: acquire), but its string value is `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"` (past tense: acquired). This is semantically confusing -- the error is returned when acquisition *fails*, not when the lock *has been* acquired. The constant is also reused for release failures (`distlock.go:54`), token generation failures (`distlock.go:171`), and actual lock contention (`distlock.go:113` uses a separate `ErrAdapterRedisLockTimeout`), which further muddies its meaning.
- **Recommendation**: (1) Rename the string value to `"ERR_ADAPTER_REDIS_LOCK_ACQUIRE"` for consistency. (2) Consider splitting into `ErrAdapterRedisLockRelease` for release failures and keeping `ErrAdapterRedisLockAcquire` for acquisition failures, to improve error diagnostics.

### [A-08] Cache.Delete uses wrong error code `ErrAdapterRedisSet`
- **Severity**: P2
- **Category**: interface
- **File**: `adapters/redis/cache.go:57-59`
- **Description**: `Cache.Delete` wraps errors with `ErrAdapterRedisSet`, but a deletion is semantically distinct from a set operation. This misclassification would confuse error monitoring/alerting systems that aggregate by error code.
- **Recommendation**: Add a dedicated `ErrAdapterRedisDel` error code, or at minimum use a more general code. Alternatively, rename `ErrAdapterRedisSet` to `ErrAdapterRedisWrite` to cover both set and delete operations.

## Cross-Reference Verification

| Finding ID | Status | Evidence |
|-----------|--------|----------|
| P0-F11S01 | **CONFIRMED** | `distlock.go:37-42`: `Lock` struct has `value` (random token) but it is unexported and not exposed to callers. No fencing token API exists. The renewal loop (`distlock.go:136-165`) reduces the window but does not eliminate the race. See new finding A-06. |
| P1-J6 | **CONFIRMED** | Grep for `Cache.*interface` and `Lock.*interface`/`Locker.*interface` in `kernel/` and `runtime/` returned zero results. Only `kernel/idempotency.Checker` has a kernel-level interface. `Cache` and `DistLock` are concrete types only. See new finding A-01. |
| P1-J7 | **CONFIRMED** | `distlock.go:117`: `renewCtx, cancel := context.WithCancel(context.Background())` -- the renewal goroutine is detached from the caller's context. If `Release()` is never called, the goroutine runs until the renewal Lua script fails or the process exits. See new finding A-03. |
| P1-M9 | **CONFIRMED** | `client.go:33-63`: `Config` has no `PoolSize`, `MinIdleConns`, `MaxIdleConns`, or `PoolTimeout` fields. The `goredis.Options` and `goredis.FailoverOptions` structs in `NewClient` (`client.go:114-131`) do not set any pool parameters, relying entirely on go-redis defaults. See new finding A-04. |

## Verdict

**PASS_WITH_CONDITIONS**

Conditions for full pass:
1. **[Must]** A-06 / P0-F11S01: Expose fencing token on `Lock` and document limitations.
2. **[Must]** A-03 / P1-J7: Derive renewal context from caller context to prevent goroutine leaks.
3. **[Must]** A-01 / P1-J6: Define `kernel/cache` and `kernel/distlock` interfaces before any Cell depends on these components.
4. **[Should]** A-04 / P1-M9: Add connection pool configuration to `Config`.
5. **[Should]** A-07, A-08: Fix error code naming inconsistencies.
6. **[May]** A-05: Add Cluster mode support and document DistLock's single-instance limitation.
