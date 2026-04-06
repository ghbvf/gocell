# R1D-2 Security Review: adapters/redis

## Summary

The `adapters/redis` module has a sound security posture overall: Lua scripts are properly parameterized, `crypto/rand` is used correctly for lock tokens, and no hard-coded credentials exist in test files. However, the module exposes the `Config.Password` field through the public `Config()` accessor (enabling downstream logging/serialization leaks), includes key names in error messages that may leak to callers, and the distributed lock lacks a fencing token mechanism, leaving a well-known split-brain write hazard unmitigated.

## Findings

### [S01] Config().Password exposes credential to callers
- **Severity**: P1
- **Category**: credential-exposure
- **File**: `adapters/redis/client.go:185-187`
- **Description**: `Client.Config()` returns a full copy of the `Config` struct, including the plaintext `Password` field. Any caller that logs, serializes, or passes this struct to observability middleware will inadvertently expose the Redis password.
- **Exploit scenario**: A health-check HTTP handler calls `client.Config()` and marshals it to JSON for a `/debug/config` endpoint. The password is included in the response.
- **Recommendation**: Either (a) redact `Password` in the returned copy (set it to `""` or `"[REDACTED]"`), (b) remove `Config()` entirely if not needed externally, or (c) implement `fmt.Stringer`/`json.Marshaler` on `Config` to omit the password.

### [S02] Health() error exposes server address to upstream callers
- **Severity**: P2
- **Category**: info-leak
- **File**: `adapters/redis/client.go:162-165`
- **Description**: The `Health()` method wraps the ping error with `fmt.Sprintf("redis: health check failed (addr=%s)", c.config.Addr)`. If this error propagates to an HTTP response (e.g., a `/healthz` endpoint returning the error string), the internal Redis address is disclosed to external clients.
- **Exploit scenario**: An attacker probes the health endpoint and receives `redis: health check failed (addr=redis-master.internal:6379)`, revealing internal infrastructure topology.
- **Recommendation**: Log the address at `slog.Error` level server-side, but return a generic error message to callers (e.g., `"redis: health check failed"`). The wrapped `err` from go-redis may also contain connection details; ensure it is not surfaced to HTTP responses.

### [S03] Cache/idempotency key names leaked in error messages
- **Severity**: P2
- **Category**: info-leak
- **File**: `adapters/redis/cache.go:37-38`, `cache.go:48`, `cache.go:58`, `cache.go:73`, `cache.go:78`, `idempotency.go:44`, `idempotency.go:56`, `idempotency.go:69`
- **Description**: Every error message in `Cache` and `IdempotencyChecker` includes the full key via `fmt.Sprintf("... (key=%s)", key)`. If keys contain tenant identifiers, user IDs, or session tokens (e.g., `session:user:abc123`), these may leak through error chains to API responses or third-party error trackers.
- **Exploit scenario**: A cache miss triggers an error that includes `key=session:jwt:eyJhbG...`. This gets serialized into a Sentry event visible to the ops team, or worse, returned in a 500 error body.
- **Recommendation**: Log keys at `slog.Debug`/`slog.Warn` level server-side. In the `errcode.Wrap` message, either omit the key entirely or truncate/hash it. The project error-handling rules (`.claude/rules/gocell/error-handling.md` rule 5) already mandate that 500 errors must not expose internal details.

### [S04] No key namespace/prefix enforcement -- cross-tenant key collision
- **Severity**: P2
- **Category**: injection
- **File**: `adapters/redis/cache.go` (all public methods), `adapters/redis/idempotency.go` (all public methods), `adapters/redis/distlock.go:96`
- **Description**: All key parameters are passed through directly to Redis commands with no validation, prefix enforcement, or character filtering. If multiple tenants share the same Redis DB (which is the default `DB: 0`), there is no structural protection against one tenant reading/writing another tenant's keys.
- **Exploit scenario**: Tenant A crafts a cache key `tenant_b:config:secrets` and retrieves Tenant B's cached configuration. Or a malicious key containing binary data is used to confuse monitoring/logging.
- **Recommendation**: Introduce a key-prefix strategy at the `Cache`/`DistLock`/`IdempotencyChecker` constructor level (e.g., `NewCache(client, prefix string)`). Validate that keys do not contain null bytes or newlines. Consider per-tenant DB isolation or key-prefix scoping.

### [S05] Default localhost:6379 fallback is unsafe for production
- **Severity**: P1
- **Category**: unsafe-default
- **File**: `adapters/redis/client.go:82-84`
- **Description**: When `Config.Addr` is empty and mode is `ModeStandalone`, `defaults()` silently sets `Addr` to `localhost:6379`. In containerized production environments, this could connect to an unrelated Redis instance running on the same host, or to an attacker-controlled service if the port is hijacked.
- **Exploit scenario**: A misconfigured deployment omits the Redis address environment variable. The service silently connects to `localhost:6379`, which in a shared Kubernetes pod could be a sidecar or a hostile container, sending credentials and data to the wrong destination.
- **Recommendation**: Remove the `localhost:6379` default. Require `Addr` to be explicitly configured; return an error from `NewClient` if `Addr` is empty in standalone mode. This aligns with **P1-K9** from the existing findings.

### [S06] DistLock lacks fencing token -- stale lock holder can corrupt protected resources
- **Severity**: P0
- **Category**: crypto
- **File**: `adapters/redis/distlock.go:37-43` (Lock struct), `distlock.go:96-133` (Acquire)
- **Description**: The `Lock` struct contains a random token used only for ownership verification during release/renewal. It does **not** expose a monotonically increasing fencing token that downstream resources (databases, queues, etc.) can use to reject stale writes. If the lock TTL expires (e.g., due to a GC pause or network partition) and a new holder acquires the lock, the old holder's in-flight writes proceed unchecked because the protected resource has no way to verify lock ownership.
- **Exploit scenario**: Holder A acquires the lock and begins a multi-step write to PostgreSQL. A long GC pause causes the lock TTL to expire. Holder B acquires the lock and begins its own writes. Holder A resumes and completes its writes, corrupting Holder B's invariant. The renewal goroutine cannot prevent this if the pause exceeds the TTL.
- **Recommendation**: (1) Add a `FencingToken() int64` method to `Lock` that returns a monotonically increasing token (e.g., from a Redis INCR on a companion key). (2) Document that consumers of `DistLock` MUST pass the fencing token to downstream stores and those stores MUST reject operations with a stale token. (3) As a defense-in-depth measure, consider having `Lock` expose an `IsValid() bool` that checks whether the lock is still held before proceeding with writes. This confirms **P0-F11S01**.

### [S07] Negative TTL values not validated
- **Severity**: P2
- **Category**: unsafe-default
- **File**: `adapters/redis/distlock.go:96-99`, `adapters/redis/cache.go:45`, `adapters/redis/idempotency.go:52,65`
- **Description**: No function validates that the TTL parameter is positive. Passing a negative `time.Duration` to `go-redis` `Set`, `SetNX`, or `PEXPIRE` results in Redis treating it as "delete immediately" or "no expiry" depending on the command and version. For `DistLock.Acquire`, a negative TTL would set a lock that expires instantly, creating a false sense of mutual exclusion. For `Cache.Set`, a negative TTL causes the key to be deleted immediately after creation (Redis >= 2.6.12: `SET key value PX -1` returns error; but via go-redis the behavior may vary).
- **Exploit scenario**: A caller accidentally passes `-1 * time.Second` as a cache TTL. The key is immediately expired or an error is silently swallowed, causing data loss or cache misses interpreted as "not found."
- **Recommendation**: Validate TTL at the public API boundary. For `DistLock.Acquire`, reject `ttl <= 0` with a clear error. For `Cache.Set`/`SetJSON`, either reject negative TTL or document that zero means no expiry and negative is invalid.

### [S08] slog.Info in NewClient does not log password (VERIFIED SAFE)
- **Severity**: N/A (informational -- no issue found)
- **Category**: credential-exposure
- **File**: `adapters/redis/client.go:145-148`
- **Description**: The `slog.Info("redis: connected", ...)` call logs `mode`, `addr`, and `db` but does **not** log `cfg.Password`. This is correct. However, for Sentinel mode, the logged `addr` will be the empty string (since `cfg.Addr` is not set in Sentinel mode), while `SentinelAddrs` is not logged, creating a minor observability gap but no security issue.
- **Recommendation**: No action required for security. For observability, consider logging `SentinelAddrs` (addresses are not sensitive) when in Sentinel mode.

### [S09] No credential leaks in test files (VERIFIED SAFE)
- **Severity**: N/A (informational -- no issue found)
- **Category**: credential-exposure
- **File**: `adapters/redis/integration_test.go`, `adapters/redis/mock_test.go`
- **Description**: Integration tests use testcontainers with no password configured (`Config` passed with no `Password` field). Mock tests use an in-memory mock with no credentials. No hard-coded passwords, tokens, or secrets appear in any test file. The `integration_test.go` file is behind a `//go:build integration` tag, so it does not run in CI by default.
- **Recommendation**: No action required.

## Cross-Reference Verification

| Finding ID | Status | Evidence |
|-----------|--------|----------|
| P0-F11S01 | **CONFIRMED** | `Lock` struct at `distlock.go:37-43` contains only `rdb`, `key`, `value`, `cancel`. No fencing token field exists. `Acquire()` returns a `Lock` with a random ownership token but no monotonic fencing token. See finding S06 above. |
| P1-K9 | **CONFIRMED** | `client.go:82-84`: `if c.Addr == "" && c.Mode == ModeStandalone { c.Addr = "localhost:6379" }`. The default is applied silently with no warning. See finding S05 above. |

## Verdict

**PASS_WITH_CONDITIONS**

Conditions for full PASS:
1. **[P0 S06]** Implement fencing token mechanism or prominently document the lock safety boundary so consumers understand the hazard and apply their own fencing.
2. **[P1 S01]** Redact `Password` from the `Config()` return value to prevent accidental credential exposure.
3. **[P1 S05]** Remove the `localhost:6379` default; require explicit address configuration.
4. **[P2 S02/S03]** Review error message content to ensure internal details (addresses, key names) do not propagate to HTTP responses per project error-handling rules.
5. **[P2 S04]** Evaluate key-prefix scoping strategy for multi-tenant deployments.
6. **[P2 S07]** Add TTL validation at public API boundaries.
