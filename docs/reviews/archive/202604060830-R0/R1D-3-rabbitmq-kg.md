# R1D-3: adapters/rabbitmq -- Kernel Guardian Review

**Reviewer role**: Kernel Guardian
**Scope**: `adapters/rabbitmq/` (7 .go files)
**Date**: 2026-04-06

---

## 1. File Inventory

| File | LOC (approx) | Purpose |
|------|-------------|---------|
| doc.go | 8 | Package doc; declares Publisher/Subscriber/ConsumerBase scope |
| connection.go | 380 | AMQP connection manager: auto-reconnect, exponential backoff, channel pool |
| publisher.go | 81 | `outbox.Publisher` implementation with confirm mode |
| subscriber.go | 257 | `outbox.Subscriber` implementation with ACK/NACK, graceful shutdown |
| consumer_base.go | 224 | ConsumerBase wrapper: idempotency + retry + DLQ routing |
| rabbitmq_test.go | 1081 | Unit tests (mocks, Connection, Publisher, Subscriber, ConsumerBase) |
| integration_test.go | 267 | Testcontainers-based integration tests (build tag `integration`) |

---

## 2. Layer Isolation Check

### 2.1 Production Imports (non-test .go files)

| File | Internal Imports | External Imports |
|------|-----------------|------------------|
| connection.go | `pkg/errcode` | `amqp091-go`, stdlib |
| publisher.go | `kernel/outbox`, `pkg/errcode` | `amqp091-go`, stdlib |
| subscriber.go | `kernel/outbox`, `pkg/errcode` | `amqp091-go`, stdlib |
| consumer_base.go | `kernel/idempotency`, `kernel/outbox` | stdlib only |

**Verdict: GREEN** -- Zero imports from `runtime/`, `cells/`, or other `adapters/` packages. The adapter depends only on `kernel/` and `pkg/`, which is the correct dependency direction per CLAUDE.md: "adapters/ implements kernel/ or runtime/ defined interfaces."

### 2.2 Test Imports

| File | Internal Imports | External Imports |
|------|-----------------|------------------|
| rabbitmq_test.go | `kernel/idempotency`, `kernel/outbox` | `amqp091-go`, `testify` |
| integration_test.go | `kernel/outbox` | `testify`, `testcontainers-go` |

**Verdict: GREEN** -- Test imports are clean; mock implementations reside within the test file.

---

## 3. Interface Contract Compliance

### 3.1 outbox.Publisher

**Kernel interface** (`kernel/outbox/outbox.go`):
```go
type Publisher interface {
    Publish(ctx context.Context, topic string, payload []byte) error
}
```

**Adapter implementation** (`publisher.go`):
- Compile-time check: `var _ outbox.Publisher = (*Publisher)(nil)` -- present on line 15.
- Signature match: `func (p *Publisher) Publish(ctx context.Context, topic string, payload []byte) error` -- exact match.
- Behavior:
  - Declares exchange idempotently (fanout, durable).
  - Enables publisher confirm mode per channel acquisition.
  - Uses `DeliveryMode: amqp.Persistent` for durability.
  - Waits for broker confirm with configurable timeout.
  - Respects context cancellation.

**Verdict: GREEN** -- Full compliance with `outbox.Publisher`.

### 3.2 outbox.Subscriber

**Kernel interface** (`kernel/outbox/outbox.go`):
```go
type Subscriber interface {
    Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error
    Close() error
}
```

**Adapter implementation** (`subscriber.go`):
- Compile-time check: `var _ outbox.Subscriber = (*Subscriber)(nil)` -- present on line 19.
- Signature match: both `Subscribe` and `Close` match exactly.
- Behavior:
  - Declares exchange + queue idempotently.
  - Binds queue to exchange.
  - Sets QoS prefetch for flow control.
  - Blocks until ctx cancellation or close signal.
  - Unmarshal failure: NACK without requeue (permanent error).
  - Handler error: NACK with requeue (transient error).
  - Handler success: ACK.
  - Graceful shutdown with WaitGroup + timeout.

**Verdict: GREEN** -- Full compliance with `outbox.Subscriber`.

### 3.3 idempotency.Checker Usage

**Kernel interface** (`kernel/idempotency/idempotency.go`):
```go
type Checker interface {
    IsProcessed(ctx context.Context, key string) (bool, error)
    MarkProcessed(ctx context.Context, key string, ttl time.Duration) error
    TryProcess(ctx context.Context, key string, ttl time.Duration) (bool, error)
}
```

**ConsumerBase usage** (`consumer_base.go`):
- Uses `TryProcess` (the atomic check-and-mark method) -- eliminates TOCTOU race. Correct.
- Does NOT use the older `IsProcessed` + `MarkProcessed` two-step pattern. Correct.
- Idempotency key format: `{ConsumerGroup}:{entry.ID}` -- matches EventBus spec `{prefix}:{group}:{event-id}` pattern (using ConsumerGroup as the combined prefix:group).
- Default TTL: `idempotency.DefaultTTL` (24h) -- matches EventBus spec.

**Verdict: GREEN** -- Correct usage of the atomic `TryProcess` method.

---

## 4. Fail-Open Semantics Analysis

**Location**: `consumer_base.go` lines 107-116.

When `TryProcess` returns an error (e.g., Redis down), ConsumerBase sets `shouldProcess = true` and proceeds. This is **fail-open** behavior.

**Assessment**:
- **Correct for at-least-once semantics**: Dropping messages when the idempotency store is unavailable would silently lose events, which is worse than potential duplicate processing.
- **Risk is bounded**: The handler itself should be idempotent at the business level (domain invariant), so temporary duplicate processing during Redis downtime is recoverable.
- **Observability**: The `slog.Warn` log includes `event_id`, `topic`, `consumer_group`, and the error, which satisfies the observability spec.

**Verdict: GREEN** -- Fail-open is the right choice for at-least-once messaging. Documented in code comments.

---

## 5. Consistency Level Support

### 5.1 L2 (OutboxFact) Support

The L2 chain is: business write + outbox write in same DB tx (postgres/OutboxWriter) -> relay polls and publishes via Publisher -> subscriber consumes.

| Component | Role | Status |
|-----------|------|--------|
| `postgres.OutboxWriter` | Atomic write in tx | Exists, implements `outbox.Writer` |
| `postgres.OutboxRelay` | Poll + publish | Exists, uses `outbox.Publisher` |
| `rabbitmq.Publisher` | Broker delivery with confirms | Exists, implements `outbox.Publisher` |
| `rabbitmq.Subscriber` | Consume from broker | Exists, implements `outbox.Subscriber` |
| `rabbitmq.ConsumerBase` | Idempotency + retry + DLQ | Exists, uses `idempotency.Checker` |

**Verdict: GREEN** -- The full L2 chain is wired: `Writer -> Relay -> Publisher -> Broker -> Subscriber -> ConsumerBase -> Handler`. All interfaces are kernel-defined and adapter-implemented.

### 5.2 L3 (WorkflowEventual) Support

L3 extends L2 with cross-cell eventual consistency. The RabbitMQ adapter provides the transport layer. Saga/workflow orchestration is out of scope for this adapter but the transport primitives (publish, subscribe, DLQ) are sufficient.

**Verdict: GREEN** -- Transport primitives present. Saga orchestration is a runtime/cells concern, not adapter.

---

## 6. Message Semantics

### 6.1 Delivery Guarantee: At-Least-Once

| Mechanism | Evidence |
|-----------|----------|
| Publisher confirms | `ch.Confirm(false)` + `NotifyPublish` channel wait (publisher.go:44-48) |
| Persistent messages | `DeliveryMode: amqp.Persistent` (publisher.go:53) |
| Manual ACK | `autoAck=false` in `ch.Consume` (subscriber.go:121) |
| ACK after handler | `ch.Ack` only after `handler()` returns nil (subscriber.go:212) |
| NACK+requeue on error | `ch.Nack(tag, false, true)` on handler error (subscriber.go:204) |
| NACK without requeue on unmarshal | `ch.Nack(tag, false, false)` on bad payload (subscriber.go:183) |

**Verdict: GREEN** -- At-least-once is correctly implemented. ACK timing is after business logic completes, which is the correct pattern per EventBus spec.

### 6.2 Not Exactly-Once

The adapter provides at-least-once delivery. Exactly-once is achieved at the application layer via ConsumerBase's `TryProcess` idempotency check. This is the correct architectural split:
- Transport layer: at-least-once (adapter responsibility)
- Application layer: effectively-once via idempotency (ConsumerBase + redis Checker)

---

## 7. EventBus Specification Compliance

### 7.1 Consumer Declaration Format

**Spec requirement** (CLAUDE.md EventBus):
```go
// Consumer: cg-{service}-{event-type}
// Idempotency key: {prefix}:{group}:{event-id}, TTL 24h
// ACK timing: after business logic + idempotency key written
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
```

**Subscriber** (`subscriber.go` lines 76-79):
```go
// Consumer: cg-{QueueName}-{topic}
// Idempotency key: handled by ConsumerBase (not in Subscriber)
// ACK timing: after handler returns nil
// Retry: transient errors -> NACK+requeue / permanent errors -> handled by ConsumerBase DLQ
```

**ConsumerBase** (`consumer_base.go` lines 69-72):
```go
// Consumer: cg-{ConsumerGroup}-{topic}
// Idempotency key: {ConsumerGroup}:{event-id}, TTL 24h
// ACK timing: after business logic + idempotency key written
// Retry: transient errors -> NACK+backoff / permanent errors -> dead letter
```

**Verdict: GREEN** -- Both Subscriber and ConsumerBase carry the declaration block.

### 7.2 Dead-Letter Routing

| Requirement | Status |
|-------------|--------|
| L2 consumer must have DLQ | ConsumerBase routes to `{topic}.dlq` or custom DLQTopic |
| DLQ messages must be observable | `slog.Error` on every dead-letter routing (line 216) with event_id, topic, dlq_topic, consumer_group, error, retry_count |
| PermanentError -> DLQ immediately | Yes, no retry (line 133-142) |
| Retry exhausted -> DLQ | Yes (lines 163-174) |
| Unmarshal failure -> NACK without requeue | Handled at Subscriber level (line 183), not by ConsumerBase |

**Verdict: YELLOW** -- The unmarshal failure path in Subscriber NACKs without requeue but does NOT route to a DLQ topic. The EventBus spec says `unmarshal failure -> deadLetter(ctx, msg, err)`. Currently, these messages are simply rejected by the broker. If the queue has no RabbitMQ-native DLX (dead-letter exchange) configured, these messages are lost.

### 7.3 Stream Naming

- Exchange names are user-supplied topic strings.
- Queue names default to topic name or are user-configured.
- Consumer tags follow `cg-{queue}-{topic}` pattern.
- No hardcoded stream/exchange names observed; constants are deferred to callers.

**Verdict: GREEN** -- Naming conventions are consistent and configurable.

### 7.4 Event Payload

- `entry.ID` is used as the canonical idempotency identifier (per kernel/outbox/outbox.go doc).
- Payload changes are transparent ([]byte pass-through).
- Metadata is a `map[string]string` with x-death enrichment on DLQ.

**Verdict: GREEN**

---

## 8. Code Quality from Guardian Perspective

### 8.1 Error Handling

| Check | Status |
|-------|--------|
| Uses `pkg/errcode` (not bare `errors.New`) | All 5 error codes use `errcode.Code` constants |
| Error wrapping with context | All errors wrapped via `errcode.Wrap` with descriptive messages |
| No `_ = someFunc()` for errors | No suppressed errors in production code |
| Structured logging on errors | All slog calls include structured fields |

**Verdict: GREEN**

### 8.2 Test Coverage

Overall: **78.7%** (unit tests only, excluding integration).

| Component | Coverage | Comment |
|-----------|----------|---------|
| ConsumerBase.Wrap | 100% | All paths: success, duplicate, transient retry, permanent error, DLQ, idempotency failure, context cancel |
| Publisher.Publish | 85.7% | Missing: context deadline branch when confirm and cancel race |
| Subscriber.Subscribe | 76.0% | Missing: consume error path |
| connection.reconnectWithBackoff | 0% | Reconnect is tested only at integration level |
| connection.reconnectLoop | 38.1% | Partial coverage of reconnect state machine |

The 78.7% is below the 80% minimum specified in CLAUDE.md. The gap is primarily in `connection.go` reconnect logic (0% on `reconnectWithBackoff`).

**Verdict: YELLOW** -- 78.7% < 80% threshold. The gap is in reconnect code paths which are inherently hard to unit test, but a mock-based reconnect scenario could push it over 80%.

### 8.3 Confirm Mode Re-enablement

**Finding (P2)**: Publisher calls `ch.Confirm(false)` on every `Publish()` call. When a channel is returned from the pool and reused, confirm mode is re-enabled unnecessarily. While RabbitMQ tolerates repeated `Confirm(false)` calls, this is an efficiency concern.

**Impact**: Minor performance overhead on high-throughput publish paths. Not a correctness issue because `Confirm(false)` is idempotent on AMQP channels.

---

## 9. Findings Summary

### P1 -- Must Fix (0 items)

None.

### P2 -- Should Fix (2 items)

| ID | Finding | Location | Recommendation |
|----|---------|----------|----------------|
| KG-RMQ-01 | Unmarshal failure not routed to DLQ | subscriber.go:177-188 | When unmarshal fails, publish the raw delivery body to `{topic}.dlq` with `x-death-reason: unmarshal_failure` metadata instead of NACK-without-requeue. This aligns with the EventBus spec's `deadLetter(ctx, msg, err)` pattern and prevents message loss when no native RabbitMQ DLX is configured. |
| KG-RMQ-02 | Unit test coverage 78.7% < 80% | rabbitmq_test.go | Add a mock-based reconnect test scenario targeting `reconnectWithBackoff` to push coverage above 80%. |

### P3 -- Advisory (3 items)

| ID | Finding | Location | Recommendation |
|----|---------|----------|----------------|
| KG-RMQ-03 | Confirm mode re-enabled per Publish | publisher.go:44 | Consider enabling confirm mode once at channel creation and caching it, or track confirm state in a wrapper. Low priority since `Confirm(false)` is idempotent. |
| KG-RMQ-04 | TryProcess marks key before handler succeeds | consumer_base.go:107 | If the handler fails all retries and goes to DLQ, the idempotency key remains claimed. This is correct for DLQ semantics (prevents reprocessing the same failed message by other consumers), but should be explicitly documented. Currently documented only in test comments (rabbitmq_test.go:972-975). |
| KG-RMQ-05 | DLQ publish failure is silently swallowed | consumer_base.go:205-213 | If DLQ publish fails, the error is logged but the original message is still ACKed (ConsumerBase.Wrap returns nil). Consider returning an error to trigger NACK+requeue, or add a DLQ failure metric counter. |

---

## 10. Constraint Checklist

| Constraint | Status | Evidence |
|------------|--------|----------|
| Layer isolation: no upward dependency | GREEN | Zero imports from `runtime/`, `cells/`, or `adapters/` (Grep verified) |
| Adapter implements kernel interface | GREEN | `outbox.Publisher`, `outbox.Subscriber` compile-time checks present |
| `pkg/errcode` usage (no bare `errors.New`) | GREEN | All 5 error codes defined as `errcode.Code` constants |
| Structured `slog` logging | GREEN | All log calls use structured fields (topic, event_id, error, etc.) |
| At-least-once delivery | GREEN | Publisher confirms + manual ACK after handler |
| Idempotency via `TryProcess` (atomic) | GREEN | Eliminates TOCTOU race vs. separate IsProcessed/MarkProcessed |
| DLQ for L2 consumers | YELLOW | ConsumerBase routes to DLQ; Subscriber unmarshal path does not |
| EventBus consumer declaration format | GREEN | Both Subscriber and ConsumerBase carry declaration blocks |
| Coverage >= 80% | YELLOW | 78.7% (1.3% gap, primarily reconnect code) |

---

## 11. Verdict

The `adapters/rabbitmq` module is architecturally sound with correct layer isolation, full kernel interface compliance, and well-implemented at-least-once delivery semantics. The ConsumerBase correctly uses the atomic `TryProcess` method and implements proper retry+DLQ routing with observability.

Two P2 items require attention:
1. The unmarshal failure path should route to DLQ rather than relying on broker-native DLX configuration.
2. Unit test coverage should be pushed above the 80% threshold.

No P1 (blocker) findings. The module is fit for purpose as the L2/L3 transport adapter.
