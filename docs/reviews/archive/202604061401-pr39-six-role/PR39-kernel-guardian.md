# PR39 Kernel Guardian Review

## Verdict

**Blocked** on contract mismatch.

## Findings

### K-01 | P1 | `DistLock` documentation and implementation disagree on the actual contract

- Files: `adapters/redis/doc.go`, `adapters/redis/distlock.go`
- Evidence:
  - The new docs say callers should use `Lock.FenceToken()` for correctness-critical paths.
  - The code issues a fresh token per call, not per acquisition.
  - The method comment explicitly claims the token is "unique per acquisition", which is false.
- Why this matters: this is a contract bug, not just a comment bug. Kernel-level infrastructure guidance depends on the API saying exactly what is safe. Right now the docs narrow the safety boundary in prose but then re-expand it by recommending a broken primitive.
- Required fix: either fix the implementation to uphold the contract or downgrade the docs so they do not recommend the current API for correctness.

### K-02 | P1 | `DistLock` renewal still ignores caller cancellation

- File: `adapters/redis/distlock.go`
- Evidence:
  - `Acquire(ctx, ...)` still does `context.WithCancel(context.Background())`.
  - The method comment still says renewal lasts until `Release()` or context cancellation.
- Why this matters: infrastructure contracts must compose with caller lifecycle. The implementation still creates an internal lease manager that is not bound to the caller's context.
- Required fix: derive renewal from the incoming `ctx`, not `context.Background()`.
