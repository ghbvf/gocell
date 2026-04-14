# R1D-2: adapters/redis Domain Review

| Field | Value |
|---|---|
| Role | S2 Domain / Semantics |
| Scope | `adapters/redis/` |
| Baseline commit | `5096d4f` |
| Evidence base | Current source and tests only |

## Summary

The package gets one important semantic choice right: `IdempotencyChecker.TryProcess()` exists and is atomic. However, the module still exposes two API contracts whose meaning is not safe enough for their names: `DistLock` and `Cache`.

## Findings

### D-01 | P0 | `DistLock` does not provide a fencing-capable lock contract

- Files: `adapters/redis/distlock.go:35-41`, `adapters/redis/distlock.go:90-132`, `adapters/redis/doc.go:3-7`
- Evidence: the returned `Lock` only holds `{key, value, cancel}`. The `value` is used internally for release/renew ownership checks but is never surfaced as a monotonic token that downstream writers can validate.
- Semantic gap: once TTL expires, holder A may continue executing side effects while holder B has already acquired the same lock. Redis ownership checks stop stale delete/renew operations, but they do not fence stale business writes.
- Recommendation: either narrow the contract to "best-effort single-node lease" or expose fencing semantics and require downstream consumers to validate them.

### D-02 | P1 | `Cache.Get()` cannot distinguish a miss from a stored empty value

- Files: `adapters/redis/cache.go:29-40`, `adapters/redis/cache.go:63-80`
- Evidence: cache miss returns `("", nil)` and JSON miss returns `(zeroValue, nil)`.
- Why it matters: if a caller intentionally stores `""`, `0`, `false`, or an empty struct payload, the read side cannot tell whether the value was absent or present. That is a semantic ambiguity in the public API, not just a convenience tradeoff.
- Recommendation: return `(value, found, error)` or a typed result object.

### D-03 | P1 | idempotency TTL semantics still allow permanent dedupe keys

- Files: `adapters/redis/idempotency.go:49-71`
- Evidence: `MarkProcessed()` and `TryProcess()` both pass `ttl` directly to Redis `SetNX`. In Redis, `ttl=0` means no expiry.
- Why it matters: a caller can silently create immortal dedupe keys and change the semantics from "bounded replay suppression" to "forever reject this key".
- Recommendation: reject `ttl <= 0` or normalize it to `idempotency.DefaultTTL`.

## Verdict

Domain signoff is blocked by `DistLock`. `IdempotencyChecker` is directionally correct, but the package still exposes ambiguous semantics that are too easy to misuse in business code.
