# Plan — 217 Trace Metadata Bridge

## Goal

Complete `CID-01` and `META-BRIDGE-01` by making HTTP observability context flow automatically across the outbox boundary into consumer handler context, without changing payload schemas, contracts, or public kernel interfaces.

## In Scope

- Auto-inject `trace_id`, `request_id`, and `correlation_id` from `context.Context` into `outbox.Entry.Metadata` on the outbox write path.
- Auto-restore those keys from `outbox.Entry.Metadata` back into consumer handler context.
- Preserve existing business metadata and RabbitMQ-added `topic` metadata.
- Add focused tests that fail first, then implement the behavior.
- Update README and public docs to define the capability boundary.

## Out Of Scope

- `TRACE-PROP-01` (`traceparent` / `b3` inbound extraction).
- Payload, contract, API, or version changes.
- Generic context dumping into metadata.
- Eventbus/direct-publish parity outside the outbox write path.
- New dependencies.

## Implementation Strategy

### Work Package 1 — Shared Bridge Helper

Add a small shared helper around existing outbox abstractions:

- Define reserved metadata keys and merge rules.
- Provide `context -> metadata` merge logic.
- Provide `metadata -> context` restore logic.
- Provide a `TopicHandlerMiddleware` for consume-side context restore.

Planned location:

- `kernel/outbox/` for reserved-key ownership and subscriber middleware.

Constraints:

- No interface changes.
- No `runtime/` or `adapters/` imports from `kernel/`.
- Only `pkg/ctxkeys` may be used for context storage.

### Work Package 2 — Publish-Side Injection

Extend the PostgreSQL outbox writer so that metadata is automatically bridged before JSON serialization:

- `Write(ctx, entry)`
- `WriteBatch(ctx, entries)`

Rules:

- Existing user metadata wins if a reserved key is already explicitly set.
- Missing reserved keys are filled from context when present.
- No synthetic `trace_id` is created when tracing is disabled.

Planned files:

- `adapters/postgres/outbox_writer.go`
- `adapters/postgres/outbox_writer_test.go`

### Work Package 3 — Consume-Side Restore

Wire a single subscriber middleware into bootstrap so all registered event handlers receive restored context automatically:

- Wrap the configured subscriber with `outbox.SubscriberWithMiddleware` before `eventrouter.New(sub)`.
- Middleware restores `trace_id`, `request_id`, and `correlation_id` from `Entry.Metadata`.
- Preserve existing handler behavior, retry semantics, and receipt settlement.

Planned files:

- `runtime/bootstrap/bootstrap.go`
- `runtime/bootstrap/bootstrap_test.go`
- `kernel/outbox/outbox_test.go`
- `adapters/rabbitmq/rabbitmq_test.go`

### Work Package 4 — Logging and Docs

Close the loop for framework users:

- Ensure relevant logging paths surface restored context or explicitly carry the bridged IDs.
- Document reserved metadata keys and capability scope.

Planned files:

- `runtime/observability/logging/logging.go` and tests if needed
- `README.md`
- `kernel/outbox/outbox.go` comments

## TDD Order

1. Add failing helper tests.
2. Add failing outbox writer tests.
3. Add failing bootstrap / subscriber tests.
4. Add failing consumer/logging tests only if the previous steps still leave CID-01 incomplete.
5. Implement in the same order.
6. Run focused tests after each edit cycle.
7. Finish with package-level and repo-level verification.

## Verification Commands

Initial focused loop:

```bash
go test ./kernel/outbox ./adapters/postgres ./runtime/bootstrap ./adapters/rabbitmq -count=1
```

After implementation:

```bash
go test ./kernel/outbox ./adapters/postgres ./runtime/bootstrap ./adapters/rabbitmq ./tests/integration -count=1
go build ./...
go test ./... -count=1
```

## Risks And Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Scope creep into `TRACE-PROP-01` | Delays delivery and mixes two backlog items | Keep `traceparent`/`b3` extraction out of this branch and document the boundary. |
| Breaking existing metadata | Regresses business handlers | Merge only missing reserved keys; preserve existing user metadata and `topic`. |
| Consumer behavior regression | Retry / DLX / receipt semantics regress | Keep restore logic in middleware and verify existing RabbitMQ receipt tests still pass. |
| Hidden dev/prod divergence | Some logs still miss correlation fields | Add targeted tests for the consumer path and update logging only where required by acceptance. |
