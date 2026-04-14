# R1D-2: adapters/redis Product Review

| Field | Value |
|---|---|
| Role | S6 Product / Acceptance |
| Scope | `adapters/redis/` |
| Baseline commit | `5096d4f` |
| Evidence base | Current source, tests, and direct integration usage only |

## User-Facing Acceptance Questions

1. Can this package be trusted as a general Redis infrastructure adapter for production features?
2. Are the exported capabilities clear enough that a cell developer can use them safely without reading the implementation?
3. Does the configuration surface cover common real-world deployment needs?

## Findings

### P-01 | P0 | `DistLock` is not acceptable as a general-purpose product capability yet

- Files: `adapters/redis/doc.go:3-7`, `adapters/redis/distlock.go:64-165`
- Evidence: the package markets `DistLock` as a first-class exported capability, but the current API does not protect consumers from stale-holder writes after lease expiry.
- Product consequence: a consumer reading the docs can reasonably assume "distributed lock" is safe for critical business serialization when it is not.
- Recommendation: either downgrade the capability description to a narrow lease helper or harden the contract with fencing semantics.

### P-02 | P1 | configuration surface is too thin for realistic deployments

- Files: `adapters/redis/client.go:32-63`, `adapters/redis/client.go:112-131`
- Evidence: config exposes no pool sizing, no ACL username, no TLS, and no validation for required sentinel fields before dialing.
- Product consequence: teams can clear local development, then hit capability gaps in staging or production the moment they need secured or tuned Redis.
- Recommendation: expand configuration to the minimum viable production surface or explicitly scope this package to simple/private deployments.

### P-03 | P1 | `Cache` read semantics are too ambiguous for consumer code

- Files: `adapters/redis/cache.go:29-40`, `adapters/redis/cache.go:63-80`
- Evidence: cache miss and zero/empty stored values collapse into the same return shape.
- Product consequence: callers cannot build correct behavior on top of the adapter without extra conventions outside the API.
- Recommendation: return an explicit presence bit.

## Positive Notes

- Constructor-time `Health()` gives fast feedback instead of letting a dead Redis connection fail much later.
- `IdempotencyChecker` already exposes `TryProcess()`, which is the right primitive for consumer-facing exactly-once-ish behavior.
- Default unit coverage is healthy for a package of this size.

## Verdict

Product acceptance is **blocked**. The package is useful as a low-level building block, but it is not ready to be presented as a broadly safe Redis adapter until lock semantics and deployment surface are made less misleading.
