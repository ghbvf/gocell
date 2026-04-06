# PR39 Six-Role Review Synthesis

| Field | Value |
|---|---|
| PR | `ghbvf/gocell#39` |
| Title | `fix(adapters/redis): R1D-2 review — P0 fencing token + 6 P1 fixes` |
| Head | `c2f120cccb15be138f99a16f0e59a100a3421a8b` |
| Review date | `2026-04-06 14:01` |
| Method | Independent six-role review from PR patch and head files only |

## Overall verdict

**Blocked.**

The PR improves several smaller issues, but it introduces or leaves open two P0 problems:

1. `src/adapters/redis/distlock.go`: the new `Lock.FenceToken()` is generated lazily via unconditional `INCR`, so the token is not bound to lock acquisition or current ownership. A stale holder can mint a newer token after losing the lease, which defeats the entire fencing design.
2. `src/adapters/rabbitmq/subscriber.go`: the new shutdown path NACKs with `requeue=false` whenever `ctx.Err() != nil`. With the default empty `DLXExchange`, RabbitMQ drops the message instead of requeueing it. That is a shutdown-time data-loss regression.

Secondary issues:

- `src/adapters/redis/distlock.go`: renewal is still derived from `context.Background()` instead of caller `ctx`, despite the intended fix.
- `src/adapters/rabbitmq/subscriber.go`: reconnect loop now retries on all subscription setup errors, not just broken delivery channels, which can spin on permanent topology/configuration failures and leak channels on the early-return paths.

## Role files

- `PR39-architect.md`
- `PR39-kernel-guardian.md`
- `PR39-security.md`
- `PR39-correctness.md`
- `PR39-product.md`
- `PR39-devops.md`
