# R1D-2 Kernel Guardian Review: adapters/redis

## Summary

The `adapters/redis` module correctly implements the `kernel/idempotency.Checker` contract with all three methods, and the primary consumer (`ConsumerBase`) has been migrated to use the atomic `TryProcess` path. However, there are several issues requiring attention: the `IsProcessed`+`MarkProcessed` two-step pattern remains exposed and is actively used in integration tests, TTL=0 creates permanent keys in both `MarkProcessed` and `TryProcess` without any guard, the `DistLock` lacks fencing tokens required for L3/L4 safety, and there are error code naming inconsistencies.

## Findings

### [KG-01] TTL=0 creates permanent idempotency keys (memory leak)
- **Severity**: P1
- **Category**: safety
- **File**: `adapters/redis/idempotency.go:52-58` (MarkProcessed), `adapters/redis/idempotency.go:65-71` (TryProcess)
- **Description**: Both `MarkProcessed` and `TryProcess` pass the caller-supplied `ttl` directly to Redis `SetNX`. When `ttl=0`, Redis interprets this as "no expiration", creating a permanent key. Over time, unbounded permanent keys constitute a memory leak. The kernel contract (`kernel/idempotency/idempotency.go`) specifies `DefaultTTL = 24 * time.Hour` but neither the interface nor the Redis implementation enforces a minimum. The `ConsumerBaseConfig.setDefaults()` in `adapters/rabbitmq/consumer_base.go:42-44` does default to `DefaultTTL` when zero, but direct callers of `MarkProcessed`/`TryProcess` (e.g., the `outbox_fullchain_test.go` integration test) are unprotected.
- **Recommendation**: Add a minimum TTL guard at the adapter level. If `ttl <= 0`, either substitute `idempotency.DefaultTTL` or return an error. This prevents accidental permanent key creation regardless of the caller. Example: `if ttl <= 0 { ttl = idempotency.DefaultTTL }`.

### [KG-02] TOCTOU-vulnerable two-step pattern still exposed and exercised
- **Severity**: P1
- **Category**: contract-compliance
- **File**: `adapters/redis/idempotency.go:37-47` (IsProcessed), `adapters/redis/idempotency.go:52-58` (MarkProcessed)
- **Description**: While `TryProcess` correctly eliminates the TOCTOU race via atomic `SetNX`, the old `IsProcessed` + `MarkProcessed` two-step pattern remains fully public in the `Checker` interface and is actively used in integration tests (`tests/integration/outbox_fullchain_test.go:302-314`, `adapters/redis/integration_test.go:128-150`). The `ConsumerBase.Wrap()` correctly uses `TryProcess`, but nothing prevents other callers from using the racy two-step pattern. The kernel contract defines all three methods, so the adapter cannot remove them, but the adapter should at least discourage the two-step pattern via documentation.
- **Recommendation**: (1) Add deprecation godoc comments to `IsProcessed` and `MarkProcessed` directing callers to use `TryProcess` instead. (2) Migrate integration tests (`outbox_fullchain_test.go`, `adapters/redis/integration_test.go`) to exercise `TryProcess` as the primary path, keeping `IsProcessed`/`MarkProcessed` tests only for backward-compatibility verification. (3) Long-term: propose deprecating the two methods in the kernel `Checker` interface.

### [KG-03] DistLock missing fencing token (L3/L4 safety gap)
- **Severity**: P0
- **Category**: consistency-level
- **File**: `adapters/redis/distlock.go:37-43` (Lock struct), `adapters/redis/distlock.go:96-133` (Acquire)
- **Description**: The `Lock` struct contains a random `value` field used for ownership verification during release/renewal, but this is not a monotonically increasing fencing token. In L3 (WorkflowEventual) and L4 (DeviceLatent) patterns, a lock holder may experience a GC pause or network partition, causing the lock to expire and be re-acquired by another holder. Without a fencing token that downstream resources can validate, both the old and new lock holders may simultaneously perform operations, leading to data corruption. The current random token only ensures "release what you own" semantics but provides no ordering guarantee for downstream writes.
- **Recommendation**: (1) Add a `FencingToken() int64` method to the `Lock` struct that returns a monotonically increasing value (e.g., via a Redis INCR counter on a dedicated key). (2) Document that L3/L4 callers MUST pass the fencing token to downstream stores which validate `token >= last_seen_token`. (3) Consider adding a `DistLock` interface in `kernel/` to formalize this contract.

### [KG-04] Error code constant name/value mismatch
- **Severity**: P2
- **Category**: governance
- **File**: `adapters/redis/client.go:16`
- **Description**: The constant `ErrAdapterRedisLockAcquire` has the string value `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"` (past tense). The Go identifier says "Acquire" (verb/action), but the error code string says "ACQUIRED" (past tense/state). This is confusing: the code is used for acquire failures, release failures, and token generation failures. The naming mismatch makes log/metric filtering unreliable.
- **Recommendation**: Rename the string value to `"ERR_ADAPTER_REDIS_LOCK_ACQUIRE"` to match the constant name. This is a one-line change but affects error-matching in logs and dashboards, so coordinate with observability consumers.

### [KG-05] ErrAdapterRedisLockAcquire used for both acquire and release errors
- **Severity**: P2
- **Category**: governance
- **File**: `adapters/redis/distlock.go:54` (Release), `adapters/redis/distlock.go:103,109` (Acquire), `adapters/redis/distlock.go:171` (randomToken)
- **Description**: The single error code `ErrAdapterRedisLockAcquire` is used for four distinct failure modes: (a) lock acquire SetNX failure, (b) lock acquire token generation failure, (c) lock release Eval failure, and (d) random token generation failure. This makes it impossible to distinguish release failures from acquire failures via error code alone, reducing observability and alerting precision.
- **Recommendation**: Introduce a dedicated `ErrAdapterRedisLockRelease errcode.Code = "ERR_ADAPTER_REDIS_LOCK_RELEASE"` for release-path failures (line 54). Optionally, introduce `ErrAdapterRedisLockToken` for token generation failures (line 171).

### [KG-06] Cache.Delete uses ErrAdapterRedisSet for delete failures
- **Severity**: P2
- **Category**: governance
- **File**: `adapters/redis/cache.go:57`
- **Description**: `Cache.Delete` wraps its error with `ErrAdapterRedisSet`, but a DEL operation is semantically distinct from a SET. Using the SET error code for delete failures confuses error-based alerting and monitoring.
- **Recommendation**: Introduce `ErrAdapterRedisDel errcode.Code = "ERR_ADAPTER_REDIS_DEL"` or reuse a more generic code. Alternatively, rename `ErrAdapterRedisSet` to `ErrAdapterRedisWrite` if the intent is to cover all mutation operations.

### [KG-07] MarkProcessed silently swallows SetNX=false (not-set) result
- **Severity**: P2
- **Category**: contract-compliance
- **File**: `adapters/redis/idempotency.go:52-58`
- **Description**: `MarkProcessed` calls `SetNX` and discards the boolean result (`_`). When the key already exists, `SetNX` returns `(false, nil)`, and `MarkProcessed` returns `nil`, which is documented as "no-op". However, this means a caller cannot distinguish between "successfully marked" and "already existed". While the kernel contract says `MarkProcessed` should be a no-op for existing keys (which this implements correctly), the discarded boolean could be useful for callers who want to detect double-marking for observability purposes. This is a minor contract-compliance observation, not a bug per se.
- **Recommendation**: Consider logging at Debug level when `SetNX` returns false, to aid in tracing duplicate mark attempts. No API change needed since the contract says no-op is correct.

### [KG-08] renewLoop uses background context detached from caller
- **Severity**: P2
- **Category**: safety
- **File**: `adapters/redis/distlock.go:117`
- **Description**: `Acquire` starts the renewal goroutine with `context.WithCancel(context.Background())`, completely detaching it from the caller's context. If the caller's context is cancelled (e.g., application shutdown), the renewal goroutine continues running until `Release` is explicitly called. This means: (1) the goroutine may outlive the application's graceful shutdown window, and (2) if `Release` is never called (e.g., panic in the critical section), the goroutine leaks until the lock TTL expires.
- **Recommendation**: Derive the renewal context from the caller's context using `context.WithCancel(ctx)` so that cancellation of the parent context also stops renewal. Additionally, consider returning a cleanup function or documenting that `Release` MUST be called (e.g., via `defer lock.Release(ctx)`).

## Cross-Reference Verification

| Finding ID | Status | Evidence |
|-----------|--------|----------|
| P1-M3 (TOCTOU race) | PARTIALLY RESOLVED | `TryProcess` added with atomic `SetNX` (idempotency.go:65-71). `ConsumerBase.Wrap()` uses `TryProcess` exclusively (consumer_base.go:107). However, `IsProcessed`+`MarkProcessed` remain public and are exercised in integration tests (outbox_fullchain_test.go:302-314, integration_test.go:128-150). The race is mitigated for the primary consumer path but remains exploitable for direct callers. See KG-02. |
| P1-K10 (TTL=0 permanent keys) | CONFIRMED | Neither `MarkProcessed` (line 53: `ic.rdb.SetNX(ctx, key, "1", ttl)`) nor `TryProcess` (line 66: `ic.rdb.SetNX(ctx, key, "1", ttl)`) guards against `ttl=0`. Redis `SetNX` with `expiration=0` creates a key with no TTL. The `ConsumerBaseConfig.setDefaults()` mitigates this for `ConsumerBase` consumers, but direct callers are unprotected. See KG-01. |
| P0-F11S01 (DistLock missing fencing token) | CONFIRMED | The `Lock` struct (distlock.go:37-43) has a random `value` field for ownership but no monotonically increasing fencing token. No `FencingToken()` method exists. No fencing token concept found anywhere in the codebase (`grep` for "fencing"/"FencingToken" returned zero results). See KG-03. |

## Verdict

**FAIL**

Rationale: One P0 finding (KG-03: missing fencing tokens for L3/L4 DistLock) blocks approval. The DistLock, as currently implemented, is inadequate as a coordination primitive for WorkflowEventual and DeviceLatent consistency levels because it cannot prevent stale-holder writes after lock expiry/re-acquisition. Additionally, the confirmed P1 findings (KG-01: permanent key leak, KG-02: TOCTOU pattern still exposed) represent ongoing risks that should be addressed before this module is considered production-ready for L2+ workloads.

Conditions for re-review:
1. **P0-KG-03**: Add fencing token support to `DistLock`/`Lock`, or document that the current `DistLock` is only suitable for L1/L2 (not L3/L4) and provide an alternative coordination primitive for L3/L4.
2. **P1-KG-01**: Add minimum TTL enforcement in `MarkProcessed` and `TryProcess`.
3. **P1-KG-02**: Add deprecation notices to `IsProcessed`/`MarkProcessed` and migrate integration tests to `TryProcess`.
