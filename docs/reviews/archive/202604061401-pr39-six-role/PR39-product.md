# PR39 Product Review

## Verdict

**Blocked.**

## Findings

### P-01 | P0 | PR title and docs claim a fencing-token fix that the shipped API does not actually provide

- Files: `adapters/redis/doc.go`, `adapters/redis/distlock.go`
- Product impact: consumers reading this PR will believe `DistLock` now supports the standard "lease + conditional write" safety pattern. In reality, the token can be minted late and by stale holders.
- Why this matters: this is a misleading product capability claim, not just an internal implementation detail.
- Required fix: do not ship this API shape under a "fencing token fix" banner.

### P-02 | P1 | subscriber default behavior became less safe for operators during shutdown

- File: `adapters/rabbitmq/subscriber.go`
- Evidence:
  - A default-config subscriber has no DLX.
  - The new shutdown branch uses `requeue=false`.
- Product impact: a routine shutdown under load can now lose messages unless operators discover and configure `DLXExchange` ahead of time.
- Required fix: preserve safe default behavior. Optional DLX support is fine, but default message-loss behavior is not.

### P-03 | P1 | Sentinel mode validation is still incomplete

- File: `adapters/redis/client.go`
- Evidence:
  - The PR now requires `SentinelAddrs`.
  - It still does not validate `SentinelMaster`.
- Product impact: a caller can still pass a structurally incomplete sentinel config and only discover it later through connection failure behavior.
- Required fix: fail fast when `ModeSentinel` is selected without `SentinelMaster`.
