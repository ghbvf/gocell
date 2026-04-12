# Research: Contract Runtime Closure

## Objective

Study how strong open-source projects handle three topics that directly affect this batch:

1. HTTP operation metadata and no-content semantics
2. Event identity and idempotency envelope design
3. Provider-driven contract verification

## Projects Reviewed

### 1. goa

- Focus: HTTP operation descriptors
- Relevant patterns:
  - Operation metadata carries method, path, and response status explicitly.
  - No-content responses are modeled intentionally rather than inferred from an absent schema.
  - Transport semantics are validated at the descriptor level before handler execution.
- Implication for GoCell:
  - Add explicit HTTP transport metadata under `endpoints.http`.
  - Treat `successStatus` and `noContent` as first-class fields.

### 2. Kubernetes API conventions

- Focus: REST semantics and API behavior consistency
- Relevant patterns:
  - `DELETE` success is represented as `204 No Content` when no response body is returned.
  - Transport behavior is part of the contract, not a side effect of implementation.
  - Backward-compatible field addition is preferred over breaking descriptor shape changes.
- Implication for GoCell:
  - Keep `DELETE` on `204`, do not regress to `200` plus body.
  - Add optional transport metadata in a backward-compatible way.
  - Enforce `noContent=true` and `response schema absent` as a governance rule.

### 3. Pact

- Focus: provider-driven contract verification
- Relevant patterns:
  - Runtime verification executes the real provider and validates actual responses.
  - Schema/example checks are not considered enough on their own.
  - Provider state setup is explicit, and verification asserts the real transport outcome.
- Implication for GoCell:
  - Contract tests must stop at neither `LoadByID` nor sample JSON schema validation.
  - Representative HTTP contract tests should call real handlers and assert status/body semantics.
  - Event contract tests should validate real emitted entries, not handwritten payload-only examples.

### 4. Watermill

- Focus: event identity and message envelope
- Relevant patterns:
  - Message identity is carried in a single canonical field.
  - Consumers and middleware derive deduplication behavior from that canonical identity.
  - Demo/basic examples are clearly separated from production-grade message delivery semantics.
- Implication for GoCell:
  - Keep `outbox.Entry.ID` as the single source of event identity.
  - Project it consistently into relay wire `id` and contract `event_id` semantics.
  - Do not introduce a second truth in payload or broker headers in this batch.

## Consolidated Decisions

1. Use `endpoints.http` as the namespaced home for transport metadata.
2. Add `method`, `path`, `successStatus`, and `noContent` together; do not split these across multiple rollout steps.
3. Keep `outbox.Entry.ID -> wire envelope id -> contract event_id` as the only event identity path.
4. Separate schema smoke from provider-driven verification in tests.
5. Distinguish demo behavior from durable behavior explicitly in docs and journeys.

## Anti-Patterns To Avoid

1. Empty request schema used as the only signal for `DELETE` no-body semantics.
2. Handwritten JSON examples standing in for runtime verification.
3. Direct publisher calls in an L2 path without a real emitted entry identity.
4. Warning-only initialization for a missing critical dependency that later causes runtime panic.
5. README or journey text that promises durable or consume-confirm behavior while the example is still in-memory.