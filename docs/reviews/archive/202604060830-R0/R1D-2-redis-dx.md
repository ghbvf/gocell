# R1D-2: adapters/redis DX Review

| Field | Value |
|---|---|
| Role | S5 DX / Maintainability |
| Scope | `adapters/redis/` |
| Baseline commit | `5096d4f` |
| Evidence base | Current source and tests only |

## Summary

The package is compact and easy to read. File boundaries are sensible, tests are straightforward, and the internal `cmdable` seam is useful. The DX issues are mostly about naming and API clarity rather than code sprawl.

## Findings

### X-01 | P1 | error-code symbol and wire value do not match

- File: `adapters/redis/client.go:13-20`
- Evidence: the Go symbol is `ErrAdapterRedisLockAcquire`, but the emitted string is `"ERR_ADAPTER_REDIS_LOCK_ACQUIRED"`.
- Why it matters: searching logs, docs, and source becomes needlessly confusing because the identifier says one thing and the external error code says another.
- Recommendation: align the symbol and the string value.

### X-02 | P2 | `Cache.Delete()` wraps delete failures as a set error

- File: `adapters/redis/cache.go:55-58`
- Evidence: delete errors are wrapped with `ErrAdapterRedisSet`.
- Why it matters: maintainers lose operation-level visibility in logs and telemetry, and downstream callers cannot branch cleanly on error class.
- Recommendation: introduce a delete-specific code or rename the existing taxonomy.

### X-03 | P2 | docs over-promise lock behavior compared with implementation

- Files: `adapters/redis/doc.go:3-7`, `adapters/redis/distlock.go:90-95`, `adapters/redis/distlock.go:117-126`
- Evidence: package and method comments describe a generic distributed lock with automatic renewal, while the implementation neither binds renewal to caller context nor documents lease-safety limits.
- Why it matters: future maintainers will assume stronger guarantees than the code actually provides.
- Recommendation: tighten the docs to the real contract before adding more callsites.

## Verdict

DX is serviceable but not polished. The codebase is maintainable today because it is small, not because the public surface is especially crisp. The package should fix naming and contract clarity before wider adoption.
