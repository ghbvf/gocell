# Product Acceptance Criteria — 217 Trace Metadata Bridge

## P1

1. Given an HTTP request enters through the default runtime router, when application code writes an outbox entry without manually filling observability metadata, then the framework persists `request_id` and `correlation_id` into `Entry.Metadata`, and also persists `trace_id` when tracing is enabled.
Verification: focused writer test plus end-to-end integration test.

2. Given an outbox entry already contains business metadata, when the framework injects observability metadata, then non-reserved user metadata is preserved and reserved bridge keys are only filled when missing.
Verification: helper test plus outbox writer test.

3. Given an event is delivered through the configured subscriber path, when the business handler executes, then `request_id`, `correlation_id`, and optional `trace_id` are available from handler context without manual metadata parsing.
Verification: bootstrap/subscriber test and RabbitMQ test.

4. Given the full outbox relay to RabbitMQ to subscriber path, when an event originates from HTTP context, then the bridged observability metadata survives end to end and matches the values visible to the consumer handler.
Verification: integration test.

5. Given tracing is disabled or an event does not originate from HTTP context, when the event is written and consumed, then processing still succeeds and no synthetic `trace_id` is fabricated.
Verification: focused unit and integration tests.

## P2

1. Framework users can discover the capability in public docs, including the reserved keys and the exact scope boundary.
Verification: README and package comments review.

2. Consumer-side framework logs expose enough information to correlate work across the async boundary without requiring business handlers to re-log metadata manually.
Verification: targeted logging test or code review if existing log behavior is already sufficient.

## Explicit Non-Goals

- No guarantee of upstream `traceparent` / `b3` continuity.
- No payload schema or contract changes.
- No promise that direct publish / in-memory eventbus flows are covered by this branch.
