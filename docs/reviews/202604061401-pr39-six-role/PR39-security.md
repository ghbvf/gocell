# PR39 Security Review

## Verdict

**Blocked** on one P0.

## Findings

### S-01 | P0 | stale lock holders can mint newer fencing tokens and win downstream conditional writes

- File: `src/adapters/redis/distlock.go`
- Security property broken: integrity of write serialization.
- Evidence:
  - `FenceToken()` uses unconditional `INCR`.
  - It performs no ownership check and no acquisition-time binding.
- Exploit path:
  - A stale process that already lost the lease can still call `FenceToken()` and obtain a higher token than the current owner.
  - Any downstream store that trusts larger tokens will accept the stale write.
- Impact: the new API defeats the very protection it advertises. This is worse than having no fencing API, because consumers may now deploy a broken correctness pattern.
- Required fix: token issuance must be part of successful acquisition, not a later open-ended method call.

### S-02 | P1 | shutdown path in RabbitMQ subscriber can silently drop messages by default

- File: `src/adapters/rabbitmq/subscriber.go`
- Evidence:
  - On `ctx.Err() != nil`, `processDelivery()` now calls `Nack(..., requeue=false)`.
  - `DLXExchange` is optional and defaults to empty.
- Impact: on standard queues with no dead-letter exchange configured, `requeue=false` discards the message. The new comment claiming the message will be redelivered is incorrect.
- Required fix: default shutdown path must not discard messages silently. Either keep `requeue=true` or require an explicit dead-letter configuration before using `requeue=false`.
