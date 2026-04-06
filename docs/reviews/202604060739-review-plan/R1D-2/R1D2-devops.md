# R1D-2 DevOps Review: adapters/redis

## Summary

The `adapters/redis` module provides a clean, minimal Redis adapter with health checking, distributed locking, caching, and idempotency support. However, it has notable operational gaps: no metrics/instrumentation, no connection pool tunability, an orphaned-goroutine risk on shutdown, a missing health check timeout, and several logging compliance issues against GoCell observability rules. The module is suitable for early development but requires hardening before production deployment.

## Findings

### [D-01] DistLock renewal goroutines are not cancelled on Client.Close()
- **Severity**: P1
- **Category**: shutdown
- **File**: `client.go:170-176`, `distlock.go:117-126`
- **Description**: `Client.Close()` calls `rdb.Close()` but has no mechanism to cancel outstanding `DistLock` renewal goroutines. Each `DistLock.Acquire()` spawns a goroutine with `context.WithCancel(context.Background())` at line 117. The cancel function is stored on the individual `Lock` struct and only invoked on `Lock.Release()`. If the `Client` is closed (e.g., during graceful shutdown) while locks are still held, the renewal goroutines continue running against a closed connection, producing repeated errors until the ticker fires and the `Eval` call fails.
- **Operational impact**: During k8s pod termination, leaked goroutines will emit spurious error logs, delay graceful shutdown, and may cause the SIGKILL timeout to be reached. In long-running services, accumulated leaked goroutines (from unreleased locks) constitute a goroutine leak.
- **Recommendation**: Maintain a `sync.WaitGroup` or a shared `context.Context` in `Client` that is cancelled on `Close()`. Wire this context as a parent for all renewal goroutine contexts. `Close()` should cancel the parent context and wait (with a timeout) for all renewal goroutines to drain before closing the underlying connection.

### [D-02] Health() has no independent timeout
- **Severity**: P2
- **Category**: health
- **File**: `client.go:161-167`
- **Description**: `Health()` relies entirely on the caller-supplied `context.Context`. If k8s liveness/readiness probes call `Health()` without a tight deadline, or if the HTTP handler passes a long-lived context, the PING command will block until the go-redis `ReadTimeout` (default 3s) expires. There is no defensive `context.WithTimeout` inside `Health()` itself.
- **Operational impact**: A hung Redis connection could cause liveness probes to exceed their `timeoutSeconds`, leading k8s to mark the pod as failed. Conversely, if `ReadTimeout` is configured very high (e.g., 30s for batch workloads), health checks become unacceptably slow.
- **Recommendation**: Add an internal `context.WithTimeout` (e.g., 2s) inside `Health()` to bound the PING regardless of the caller's context. Make this configurable via a `HealthTimeout` field in `Config`.

### [D-03] No connection pool configuration exposed
- **Severity**: P2
- **Category**: pool
- **File**: `client.go:33-63`, `client.go:114-131`
- **Description**: The `Config` struct exposes dial/read/write timeouts but none of the go-redis connection pool settings: `PoolSize`, `MinIdleConns`, `MaxIdleConns`, `ConnMaxIdleTime`, `ConnMaxLifetime`, `PoolFIFO`, `MaxRetries`, `MinRetryBackoff`, `MaxRetryBackoff`. The `goredis.Options` and `goredis.FailoverOptions` structs accept these, but they are left at library defaults.
- **Operational impact**: Operators cannot tune pool behavior for their workload. Under high concurrency, the default pool size (10 * NumCPU) may be insufficient or wasteful. Without `MaxRetries`, transient network blips cause immediate failures. Without `ConnMaxLifetime`, connections may accumulate in stale states behind load balancers or proxies that silently drop idle connections.
- **Recommendation**: Add pool-related fields to `Config` (at minimum: `PoolSize`, `MinIdleConns`, `MaxRetries`) and plumb them through to go-redis options. Document the defaults clearly.

### [D-04] No metrics or instrumentation for Redis operations
- **Severity**: P2
- **Category**: metrics
- **File**: (module-wide)
- **Description**: There are zero Prometheus metrics, OpenTelemetry spans, or instrumentation hooks anywhere in the module. go-redis v9 supports `redisotel` for automatic tracing and `redisprometheus` for pool metrics, but neither is integrated.
- **Operational impact**: Operators have no visibility into: connection pool utilization, command latency percentiles, error rates by command type, lock acquisition/contention rates, idempotency hit/miss ratios, or cache hit rates. This makes production debugging and capacity planning extremely difficult.
- **Recommendation**: Integrate `github.com/redis/go-redis/extra/redisotel/v9` for OpenTelemetry tracing and `github.com/redis/go-redis/extra/redisprometheus/v9` for connection pool metrics. Add application-level metrics for lock contention (acquire success/fail counters) and idempotency check rates. Consider accepting a metrics registrar interface to avoid hard-coupling.

### [D-05] slog.Error on renewal failure missing correlation fields
- **Severity**: P2
- **Category**: observability
- **File**: `distlock.go:149-151`
- **Description**: The `slog.Error("redis: lock renewal failed", "key", lock.key, "error", err)` log includes `key` and `error` but lacks correlation fields such as `lock_value` (token), any request/execution ID, or the renewal attempt count. Per GoCell observability rules, Error-level logs "must include structured correlation fields" and "prohibit bare `slog.Error("failed")`". While this log is not bare (it has fields), it is missing the correlation fields needed to trace a specific lock holder in a multi-instance deployment.
- **Operational impact**: When multiple instances hold different locks, operators cannot correlate renewal failures to specific lock acquisitions without the lock token. There is no way to distinguish repeated failures on the same lock from failures on different locks with the same key pattern.
- **Recommendation**: Add `"lock_value", lock.value` (or a truncated version) to the error log. If a request-scoped context is available, extract and log `execution_id` or `trace_id` from it. Note: the renewal goroutine currently uses `context.Background()`, which inherently strips all context values -- this ties back to D-01's recommendation of wiring a proper parent context.

### [D-06] Config.Password could be logged indirectly
- **Severity**: P2
- **Category**: observability
- **File**: `client.go:145-148`
- **Description**: The `slog.Info("redis: connected", ...)` log at line 145 includes `"mode"`, `"addr"`, and `"db"` but not `Password`, which is good. However, `Config` is a plain struct with an exported `Password` field. If any caller logs the `Config` struct (e.g., `slog.Info("config", "redis", cfg)`), the password will appear in logs. There is no `String()` or `LogValue()` method on `Config` to redact the password.
- **Operational impact**: Accidental password exposure in logs is a security incident. While the current code does not log the full struct, future developers or debug logging could inadvertently do so.
- **Recommendation**: Implement `slog.LogValuer` on `Config` to redact the `Password` field, or change `Password` to a custom `Secret` type that masks its value in `String()`/`LogValue()`.

### [D-07] Sentinel mode log message shows empty addr
- **Severity**: P3
- **Category**: observability
- **File**: `client.go:145-148`
- **Description**: When `Mode == ModeSentinel`, `cfg.Addr` is empty (Sentinel uses `SentinelAddrs`). The connect log `slog.Info("redis: connected", "addr", cfg.Addr, ...)` will show `"addr": ""`, which is misleading and provides no useful information about which Sentinel endpoints were used.
- **Operational impact**: Operators viewing logs of Sentinel-mode deployments will see a blank address, making it harder to identify which Redis cluster the client connected to.
- **Recommendation**: Conditionally log `"sentinel_addrs"` and `"sentinel_master"` when in Sentinel mode, or log `"addr"` as the Sentinel addresses.

### [D-08] Integration test container cleanup uses background context
- **Severity**: P3
- **Category**: testing
- **File**: `integration_test.go:53-56`
- **Description**: The cleanup function captures `ctx` from the top-level `context.Background()` at line 19. The `container.Terminate(ctx)` call uses this unbounded context. If the test process is killed or times out, the container may not be cleaned up promptly. Additionally, errors from both `client.Close()` and `container.Terminate()` are silently discarded with `_`.
- **Operational impact**: In CI environments, leaked testcontainers instances consume resources. The `_ = container.Terminate(ctx)` pattern means a failed cleanup is invisible.
- **Recommendation**: Use `t.Cleanup()` instead of `defer cleanup()` for more idiomatic Go test cleanup. Add `context.WithTimeout` to the terminate call. Log or `t.Log` any termination errors. Consider using `testcontainers.CleanupContainer` if available.

### [D-09] Integration tests lack Cache and IdempotencyChecker high-level API coverage
- **Severity**: P3
- **Category**: testing
- **File**: `integration_test.go`
- **Description**: `TestIntegration_CacheSetGetDelete` tests raw `cmdable` (Set/Get/Del) rather than the `Cache` struct's `Set()`/`Get()`/`Delete()`/`GetJSON()`/`SetJSON()` methods. The `IdempotencyChecker` test does not exercise `TryProcess()`. Integration tests should validate the full public API against a real Redis, not just the low-level client.
- **Operational impact**: Bugs in JSON serialization, cache miss handling, or `TryProcess` atomicity would not be caught by integration tests.
- **Recommendation**: Add integration test cases that exercise `Cache.Set()`, `Cache.Get()`, `Cache.Delete()`, `GetJSON()`, `SetJSON()`, and `IdempotencyChecker.TryProcess()`.

### [D-10] No retry/backoff on transient Redis errors
- **Severity**: P3
- **Category**: pool
- **File**: (module-wide)
- **Description**: Neither `Config` nor the go-redis options initialization sets `MaxRetries`, `MinRetryBackoff`, or `MaxRetryBackoff`. The go-redis library defaults to `MaxRetries: 3` for `ClusterClient` but `MaxRetries: 0` for standalone `Client`. This means a single transient network error (TCP reset, brief Redis restart) will immediately surface as an error to callers.
- **Operational impact**: During rolling Redis restarts or brief network partitions, all operations fail immediately rather than retrying, causing unnecessary error spikes and potential lock acquisition failures.
- **Recommendation**: Expose `MaxRetries` in `Config` and set a sensible default (e.g., 3) with exponential backoff.

## Logging Audit

| Location | Level | Content | Verdict |
|----------|-------|---------|---------|
| `client.go:139-140` | Warn | Close failure after health check | OK -- degraded path, Warn is appropriate |
| `client.go:145-148` | Info | Connected: mode, addr, db | ISSUE -- addr is empty in Sentinel mode (D-07); no password leak in current code but Config lacks LogValuer redaction (D-06) |
| `client.go:174` | Info | Connection closed | OK -- lifecycle event, Info is correct; could benefit from including addr for correlation |
| `distlock.go:58-59` | Warn | Lock already released or expired | OK -- degraded/unexpected but not error-level; the lock was still released (idempotent) |
| `distlock.go:128-130` | Debug | Lock acquired: key, ttl | OK -- diagnostic level, appropriate for Debug; no sensitive data in key pattern |
| `distlock.go:149-151` | Error | Renewal failed: key, error | ISSUE -- missing correlation fields per GoCell rules (D-05); should include lock_value and ideally trace context |
| `distlock.go:155-156` | Warn | Lock lost during renewal | OK -- degraded state, Warn is correct; the goroutine exits after this |
| `distlock.go:160-161` | Debug | Lock renewed: key, ttl | OK -- diagnostic level, appropriate for Debug |

**Notable absences:**
- `cache.go`: Zero logging. Cache misses, errors, and operations are silent. While errors are returned to callers, there is no operational visibility without caller-side logging.
- `idempotency.go`: Zero logging. Idempotency checks and marks are completely silent. Operators cannot observe duplicate detection rates.

## Verdict

**PASS_WITH_CONDITIONS**

Conditions for production readiness:

1. **Must fix (P1)**: D-01 -- DistLock goroutine lifecycle must be tied to Client shutdown to prevent goroutine leaks and enable graceful termination.
2. **Should fix before production (P2)**: D-02 (health timeout), D-03 (pool config), D-04 (metrics), D-05 (correlation fields), D-06 (password redaction).
3. **Recommended (P3)**: D-07 through D-10 improve operational quality but are not blocking.

The module's core design is sound (Lua scripts for atomicity, SET NX for idempotency, proper error wrapping with errcode). The primary gaps are in operational instrumentation and lifecycle management, which are typical for an early-stage adapter but must be addressed before carrying production traffic.
