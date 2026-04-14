# Research: Kernel Outbox Cleanup

## Upstream Comparison

| Project | Files/docs checked | Relevant pattern | Takeaway for GoCell |
| --- | --- | --- | --- |
| Watermill | `message/pubsub.go`, `message/message.go`, pub/sub docs, forwarder/outbox docs | Keep publisher abstraction minimal; ack/receipt semantics belong on the consumer/idempotency side, not the publisher interface | `Receipt` belongs closer to idempotency than to outbox publishing APIs |
| go-micro | `broker/broker.go`, `broker/memory.go`, `broker/http.go`, `broker/rabbitmq/*`, `server/subscriber.go` | Shared no-op behavior is modeled as small implementations at the abstraction boundary; ack/no-ack semantics vary by backend | Shared `NoopOutboxWriter` / `DiscardPublisher` helpers are appropriate kernel-level conveniences |
| Uber fx | `lifecycle.go`, `internal/lifecycle/lifecycle.go`, `zap/logger.go`, `config/nop.go` | Opaque `Nop*` implementations live in the owning package, and lifecycle ownership is kept near the state it manages | `Receipt` ownership should match the state machine that consumes it; no-op helpers should be first-class package types instead of repeated locals |

## Current Code Diagnosis

### Shared noop writer duplication

Current local `outbox.Writer` placeholders exist in at least these call sites:

- `src/cmd/core-bundle/auth_integration_test.go`
- `src/cells/access-core/cell_test.go`
- `src/cells/audit-core/cell_test.go`
- `src/cells/config-core/cell_test.go`
- `src/cells/order-cell/cell_test.go`
- `src/examples/sso-bff/main.go`

They all implement the same single-method no-op contract.

### Receipt ownership mismatch

Current structure:

- `kernel/outbox/outbox.go` defines `Receipt`
- `kernel/idempotency/idempotency.go` imports `kernel/outbox` only to name that interface
- `adapters/redis` implements the concrete receipt type
- `runtime/eventbus` and `adapters/rabbitmq` consume the interface as part of idempotency flow

That means the consumer-side idempotency contract is owned by the publisher package, which is the wrong dependency direction.

### Discard publisher context

Historical review context shows an `order-cell` local `discardPublisher` once existed and was later removed by `fix(order-cell): harden runtime modes` in favor of explicit `nil` handling.

The important constraint from that hardening change is still valid:

- demo mode may skip direct publish,
- but the code must not report a fake successful publish.

Therefore the cleanup should restore an explicit discard implementation only if the service still logs the path as skip/discard, not as publish success.

## Design Choice

1. Define the canonical `Receipt` interface in `kernel/idempotency`.
2. Keep `kernel/outbox` focused on publisher/writer abstractions plus shared test/demo helpers.
3. Add `NoopOutboxWriter` and `DiscardPublisher` as explicit small types in `kernel/outbox`.
4. Update `order-cell` to use the shared discard publisher while preserving the current explicit skip semantics.
5. Replace repeated local noop writers in tests/examples with the shared helper to shrink boilerplate.

## Risk Notes

- A full receipt rename across every file is mechanically safe but noisy. If churn grows too large, a temporary alias in `kernel/outbox` can keep the change focused while still moving canonical ownership to `kernel/idempotency`.
- `DiscardPublisher` must not silently turn skipped demo publishes into success logs.
- `NoopOutboxWriter` is acceptable for tests/examples, but production paths should continue to prefer explicit real writers or fail-fast validation.