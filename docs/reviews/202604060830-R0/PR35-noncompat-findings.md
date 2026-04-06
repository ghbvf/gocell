# PR #35 Review Report (Non-Compatibility Findings Only)

| Field | Value |
|-------|-------|
| PR | `#35` |
| Title | `review: R1C+R1H runtime review + naming audit + 24 P1 fixes (Wave 1+2)` |
| Scope | Non-compatibility findings only |
| Excluded | API/HTTP contract compatibility changes for code not yet launched |
| Date | `2026-04-06` |

---

## Summary

This report keeps only behavior and correctness issues that remain relevant
even under the assumption that compatibility does not matter yet.

Result: **4 findings kept**.

---

## Findings

### 1. P1 - TryProcess can silently lose requeued messages

- **Files**:
  [consumer_base.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/consumer_base.go#L106),
  [subscriber.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L197)
- **Severity**: P1
- **Why it matters**:
  `TryProcess` now claims the idempotency key before business logic runs. If the
  handler later returns an error before ACK, the subscriber NACKs and requeues
  the message. On redelivery, the same message is treated as already processed
  and is skipped, then ACKed.
- **Failure scenario**:
  Handler fails due to context cancellation, process crash, panic recovery path,
  or transient downstream failure after `TryProcess` succeeded but before
  business completion.
- **Impact**:
  At-least-once processing degrades into silent message loss.

### 2. P2 - Redis fail-open path no longer persists idempotency state

- **File**:
  [consumer_base.go](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/consumer_base.go#L107)
- **Severity**: P2
- **Why it matters**:
  When `TryProcess` errors, the code intentionally proceeds with business
  handling. However, the old post-success `MarkProcessed` safety net is gone.
  If Redis is temporarily unavailable during claim time, a successfully handled
  message leaves no idempotency marker behind.
- **Failure scenario**:
  Redis hiccups when consumption starts, business logic succeeds, the message is
  ACKed, then the event is replayed or redelivered later.
- **Impact**:
  Duplicate side effects can occur after otherwise successful processing.

### 3. P2 - PostgreSQL outbox path drops Topic routing information

- **Files**:
  [outbox.go](/Users/shengming/Documents/code/gocell/src/kernel/outbox/outbox.go#L20),
  [outbox_writer.go](/Users/shengming/Documents/code/gocell/src/adapters/postgres/outbox_writer.go#L49),
  [001_create_outbox_entries.up.sql](/Users/shengming/Documents/code/gocell/src/adapters/postgres/migrations/001_create_outbox_entries.up.sql#L1),
  [outbox_relay.go](/Users/shengming/Documents/code/gocell/src/adapters/postgres/outbox_relay.go#L196)
- **Severity**: P2
- **Why it matters**:
  `outbox.Entry` now supports a separate `Topic`, and relay publishing uses
  `RoutingTopic()`. But the PostgreSQL schema, writer, and relay scan path still
  only persist and read the old fields.
- **Failure scenario**:
  A producer writes `Entry{EventType: "event.user.created.v1", Topic: "audit.events.v2"}`
  through transactional outbox. After persistence, `Topic` is lost, so relay
  falls back to `EventType`.
- **Impact**:
  Messages can be published to the wrong broker topic/exchange.

### 4. P2 - JWT constructors panic on nil keys

- **File**:
  [jwt.go](/Users/shengming/Documents/code/gocell/src/runtime/auth/jwt.go#L25)
  and
  [jwt.go](/Users/shengming/Documents/code/gocell/src/runtime/auth/jwt.go#L66)
- **Severity**: P2
- **Why it matters**:
  `NewJWTVerifier` and `NewJWTIssuer` dereference key pointers before validating
  that the pointers themselves are non-nil.
- **Failure scenario**:
  A caller passes a nil public/private key because of a bad load path, partial
  hot reload, or missed error check upstream.
- **Impact**:
  Initialization turns into a process-level panic instead of a returned error.

---

## Excluded Compatibility Findings

These were intentionally excluded from the kept set for this report:

- Query parameter rename from `event_type` / `actor_id` to `eventType` / `actorId`
- Constructor signature changes for `NewJWTVerifier` / `NewJWTIssuer`
- `kernel/idempotency.Checker` interface expansion with `TryProcess`

Reason: the current review mode assumes the code has not launched yet, so
compatibility is not treated as a release blocker.

---

## Validation Notes

The following local verification passed during review:

- `go test ./... -count=1`
- `go test ./runtime/... ./kernel/... ./adapters/... -count=1`
- `go test -race ./runtime/... ./kernel/... -count=1`

These findings are therefore based on behavioral analysis and uncovered edge
cases rather than failing tests.
