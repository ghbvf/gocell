# PR39 DevOps Review

## Verdict

**Yellow**, with one blocking operational risk inherited from correctness.

## Findings

### D-01 | P0 | shutdown-time message loss risk makes safe rollout impossible without extra broker config

- File: `src/adapters/rabbitmq/subscriber.go`
- Operational impact:
  - Graceful shutdown during deploy or restart can discard in-flight failed messages unless every affected queue is provisioned with a DLX.
  - The new behavior is not safe by default, which raises rollout risk for the PR.
- Required fix: restore a no-loss default before merge.

### D-02 | P1 | reconnect loop can spin aggressively on permanent topology failures

- File: `src/adapters/rabbitmq/subscriber.go`
- Evidence:
  - `Subscribe()` retries forever on any `subscribeOnce()` error.
  - Healthy connections cause `WaitConnected()` to return immediately.
- Operational impact: a bad queue/exchange permission or malformed declaration can create a fast error loop, noisy logs, and repeated channel churn against the broker.
- Required fix: separate reconnectable transport errors from terminal setup errors, and add backoff if retries remain.

### D-03 | P1 | tests do not cover the broken fencing scenario they are introducing

- Files: `src/adapters/redis/distlock_test.go`
- Evidence:
  - New tests cover monotonic increase across calls and acquisitions.
  - No test covers the actual stale-holder scenario.
- Operational impact: CI will go green while the headline safety fix is still invalid.
- Required fix: add a regression test that demonstrates token issuance is tied to acquisition order, not call order.
