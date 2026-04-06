# PR39 Correctness Review

## Verdict

**Blocked** on two correctness regressions.

## Findings

### C-01 | P0 | `FenceToken()` semantics are incorrect and the new tests codify the wrong behavior

- Files: `src/adapters/redis/distlock.go`, `src/adapters/redis/distlock_test.go`, `src/adapters/redis/mock_test.go`
- Evidence:
  - `TestLock_FenceToken` asserts that calling `FenceToken()` twice on the same lock should return `1` then `2`.
  - That means the implementation is explicitly per-call, not per-acquisition.
  - The method comment claims the token is unique per acquisition.
- Why this is a correctness bug: a fencing token is meaningful only if it identifies acquisition order. The tests currently bless behavior that invalidates that property.
- Required fix: generate one token during acquisition and assert that repeated reads return the same token for the same lock object.

### C-02 | P0 | subscriber shutdown path now loses messages when handler returns error after context cancellation

- File: `src/adapters/rabbitmq/subscriber.go`
- Evidence:
  - `processDelivery()` checks `ctx.Err()` after handler failure and calls `Nack(tag, false, false)`.
  - The code comment says the message will be picked up later, but that is false without broker dead-letter configuration.
- Concrete regression:
  - `ConsumerBase.Wrap()` can return `ctx.Err()` during retry backoff on shutdown.
  - That feeds directly into the new `requeue=false` branch.
  - The message is dropped on the floor for the default subscriber config.
- Required fix: do not switch shutdown failures to `requeue=false` unless a guaranteed DLQ path exists.

### C-03 | P1 | channel acquisition failures in `subscribeOnce()` leak channels on early returns

- File: `src/adapters/rabbitmq/subscriber.go`
- Evidence:
  - After `AcquireChannel()`, the code tracks the channel.
  - On `Qos`, `ExchangeDeclare`, `QueueDeclare`, `QueueBind`, or `Consume` error, it only calls `untrackChannel(ch)` and returns.
  - It does not `Close()` the channel or return it to the connection pool.
- Impact: repeated setup failures can leak AMQP channel resources.
- Required fix: close the acquired channel on every early-return setup failure.
