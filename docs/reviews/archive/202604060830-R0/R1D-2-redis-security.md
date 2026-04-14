# R1D-2: adapters/redis Security Review

| Field | Value |
|---|---|
| Role | S4 Security / Failure Modes |
| Scope | `adapters/redis/` |
| Baseline commit | `5096d4f` |
| Evidence base | Current source and tests only |

## Summary

The package avoids a few common security mistakes: lock tokens come from `crypto/rand`, credentials are not logged, and release/renew operations are ownership-checked with Lua. The remaining security problems are about unsafe defaults and overstated safety boundaries.

## Findings

### S-01 | P0 | `DistLock` cannot protect integrity-sensitive critical sections after lease expiry

- Files: `adapters/redis/distlock.go:35-41`, `adapters/redis/distlock.go:90-132`
- Evidence: lock ownership is represented by a random token used only inside Redis scripts. No fencing token is exposed to downstream state-changing systems.
- Security impact: if the lock is used to serialize writes to a database or external system, a stale holder can continue writing after lease expiry while a new holder is already active. This is an integrity failure, not just an availability issue.
- Recommendation: do not present this primitive as sufficient for integrity-critical exclusion unless fenced writes are part of the contract.

### S-02 | P1 | empty standalone address silently falls back to `localhost:6379`

- Files: `adapters/redis/client.go:65-85`, `adapters/redis/client_test.go:12-22`
- Evidence: `Config.defaults()` injects `localhost:6379` when `Addr == ""` in standalone mode, and the test suite asserts this default.
- Security impact: missing configuration turns into a live connection attempt against the local machine instead of a hard startup failure. That can hide deployment mistakes and redirect traffic to an unintended Redis instance.
- Recommendation: fail fast on missing address in production-facing constructors, or at minimum make the fallback opt-in.

### S-03 | P1 | client config has no path to TLS or ACL username-based auth

- Files: `adapters/redis/client.go:32-63`, `adapters/redis/client.go:112-131`
- Evidence: `Config` exposes only `Password`, DB, and timeout fields. `NewClient()` wires neither `Username` nor TLS settings into the underlying go-redis options.
- Security impact: hardened Redis deployments that require ACL usernames or TLS transport cannot be expressed through this adapter.
- Recommendation: add explicit TLS and ACL configuration fields, or clearly declare that this adapter is limited to non-hardened/private-network deployments.

## Verdict

Security signoff is blocked. The package has decent local safeguards, but the advertised lock capability and configuration defaults are not safe enough for production-sensitive use without clearer boundaries or stronger controls.
