# PR39 Architect Review

## Verdict

**Blocked** on one P0 and one P1.

## Findings

### A-01 | P0 | `FenceToken()` is not bound to lock acquisition or lock ownership

- Files: `src/adapters/redis/distlock.go`
- Evidence:
  - `FenceToken()` runs `INCR` on `fence:{key}` every time it is called.
  - It does not check that the lock is still owned by `l.value`.
  - It is not generated atomically with `Acquire()`.
- Failure mode:
  1. Holder A acquires the lease and stalls.
  2. Lease expires; holder B acquires a fresh lease and calls `FenceToken()`, gets token `1`.
  3. Stale holder A resumes and calls `FenceToken()` on its old `Lock`, gets token `2`.
  4. A stale write can now beat B in any downstream `WHERE fence_token < $1` guard.
- Why this matters architecturally: the PR claims to align with fencing-token practice, but the implementation violates the core requirement that the token be tied to acquisition order. The new API gives callers a false sense of safety.
- Required fix: generate and persist the fence token during successful `Acquire()`, ideally atomically with lease acquisition, and make `FenceToken()` a pure accessor.

### A-02 | P1 | subscriber reconnect loop treats permanent setup errors as reconnectable transport failures

- File: `src/adapters/rabbitmq/subscriber.go`
- Evidence:
  - `Subscribe()` loops forever on any non-nil `subscribeOnce(...)` error.
  - `subscribeOnce()` returns errors for `Qos`, `ExchangeDeclare`, `QueueDeclare`, `QueueBind`, and `Consume`, not only delivery-channel loss.
  - `WaitConnected()` returns immediately while the connection is healthy, so permanent topology/config errors become a tight resubscribe loop.
- Why this matters: transport recovery and configuration failure are different failure classes. Merging them turns a one-shot operator error into a hammer loop against the broker.
- Required fix: only reconnect on broken-channel / disconnected cases. For topology or declaration failures, return the error to the caller.
