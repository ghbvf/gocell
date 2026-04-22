# Research — 217 Trace Metadata Bridge

## Scope

- Backlog items: `CID-01 (consumer side)` + `META-BRIDGE-01 (Entry.Metadata injection)`
- Source: `docs/backlog.md`
- Worktree: `worktrees/217-trace-metadata-bridge`
- Branch: `fix/217-trace-metadata-bridge`

## Problem Statement

HTTP ingress already puts `request_id`, `correlation_id`, and optional `trace_id` into `context.Context`, but the outbox path does not automatically copy those values into `outbox.Entry.Metadata`, and the consumer path does not automatically restore them back into handler context.

Current result:

- HTTP logs can contain request and trace context.
- Outbox payload and metadata only carry what business code manually fills.
- RabbitMQ subscriber only adds `topic` into metadata.
- Consumer handlers and framework logs do not get request/correlation context automatically.

## Open Source Comparison

| Project | Evidence | Observed Pattern | Decision for GoCell |
|---------|----------|------------------|---------------------|
| OpenTelemetry Go | `otelhttp/handler.go`, `propagation/trace_context.go` | Server middleware does `Extract(header carrier)` before `Start`, and propagation APIs support map carriers for non-HTTP transports | Reuse the carrier mindset, but do not implement inbound `traceparent`/`b3` extraction here. That remains `TRACE-PROP-01`. |
| Watermill | `message/message.go`, `pubsub/gochannel/pubsub.go` | Message metadata is a headers-like envelope; context preservation is transport-specific; async flows copy metadata, not payload schema | Keep observability fields in `Entry.Metadata`, not in payload/contracts. Use a white-list merge, not a context dump. |
| Kratos | `middleware/tracing/tracing.go`, `metadata/metadata.go` | Transport middleware owns metadata extraction/injection and normalizes keys at the boundary | Put publish-side injection at the write boundary and consume-side restore at the subscriber boundary, not in cells or handlers. |

## Team Exploration Summary

### Architect

- Do not change `outbox.Entry`, `outbox.Publisher`, payload schemas, or contracts.
- Publish-side bridge should happen at the stable outbox write boundary.
- Consume-side restore should happen through `SubscriberWithMiddleware`, ideally wired once in bootstrap.
- White-list only observability keys required by this backlog.

### Kernel Guardian

- Keep the task within existing abstractions.
- Additive helpers are acceptable; interface or contract changes are not.
- Avoid pushing bridge logic into `cells/*` or `RegisterSubscriptions`.
- Tests must prove automatic behavior, not manual metadata round-trip only.

### Product Manager

- Acceptance must stop at GoCell-internal HTTP -> outbox -> consumer continuity.
- Upstream `traceparent`/`b3` continuity is explicitly out of scope.
- Framework consumers should get the bridge without writing manual metadata plumbing.
- README and public package docs must state what is automatic and what is not.

## Verified Local Gaps

### Publish Side

- `runtime/http/middleware/request_id.go` already bridges `RequestID -> CorrelationID`.
- `runtime/http/middleware/tracing.go` already creates spans and stores trace context in `ctxkeys`.
- `adapters/postgres/outbox_writer.go` serializes `entry.Metadata` as-is, with no automatic merge from context.

### Consume Side

- `adapters/rabbitmq/subscriber.go` only ensures `entry.Metadata["topic"] = topic`.
- `runtime/bootstrap/bootstrap.go` passes the raw subscriber into `eventrouter.New(sub)`.
- `kernel/outbox/SubscriberWithMiddleware` already exists and is the correct seam for cross-cutting handler wrapping.

### Logging

- `runtime/observability/logging/logging.go` extracts `trace_id`, `span_id`, `request_id`, and `cell_id` from context.
- `ConsumerBase` and subscriber log paths still use plain `slog.*` calls, so restored context would not automatically appear unless those paths use context-aware logging or explicit attributes.

## Design Decisions

1. Reserved bridge keys for this task are `trace_id`, `request_id`, and `correlation_id`.
2. `span_id` is not propagated across the async boundary.
3. Publish-side injection happens before metadata persistence, not in business services.
4. Consume-side restore happens once at the subscriber pipeline boundary, not in each consumer.
5. Existing business metadata must be preserved; bridge keys only fill missing values.
6. `TRACE-PROP-01` is out of scope. No inbound `traceparent`/`b3` extraction work is included here.
7. Direct publish / in-memory eventbus parity is recorded as residual risk, not part of this branch's acceptance.

## Residual Risks

- Code paths that publish directly through `outbox.Publisher` or `runtime/eventbus` without outbox persistence will not benefit from the publish-side bridge in this branch.
- If framework logs remain non-contextual in some consumer paths, users may still need explicit log attributes to see the restored IDs.
