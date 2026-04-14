# R1D-3: adapters/rabbitmq Architect Review

**Reviewer**: Architect  
**Date**: 2026-04-06  
**Scope**: `adapters/rabbitmq/` -- 7 files (~950 LOC prod, ~1080 LOC test)  
**Files**: connection.go (380L), publisher.go (81L), subscriber.go (257L), consumer_base.go (224L), doc.go (8L), rabbitmq_test.go (1081L), integration_test.go (267L)

---

## 1. Executive Summary

The RabbitMQ adapter is a well-structured implementation of `outbox.Publisher` and `outbox.Subscriber` with reconnect, channel pooling, ConsumerBase (idempotency + retry + DLQ), and comprehensive tests. Dependency direction is fully compliant. The TryProcess TOCTOU fix (Issue #18) is correctly implemented. However, the review surfaces **3 P0 findings** (DLQ-publish-then-ACK violation, reconnect subscriber invalidation, PermanentError detection fragility), **4 P1 findings**, and **3 P2 findings**.

| Severity | Count | Key Issue |
|----------|-------|-----------|
| P0 | 3 | DLQ publish failure silently ACKs, Subscriber channel invalidation on reconnect, PermanentError type assertion bypass |
| P1 | 4 | sanitizeURL credential leak, no TLS support, serial processDelivery, confirm mode per-publish overhead |
| P2 | 3 | No MessageId on published messages, no metrics/tracing hooks, ConsumerBase retry blocks Subscriber goroutine |

---

## 2. Dependency Compliance (GREEN)

### Import analysis (all 5 production files)

| File | Imports from gocell | Verdict |
|------|---------------------|---------|
| connection.go | `pkg/errcode` | GREEN |
| publisher.go | `kernel/outbox`, `pkg/errcode` | GREEN |
| subscriber.go | `kernel/outbox`, `pkg/errcode` | GREEN |
| consumer_base.go | `kernel/idempotency`, `kernel/outbox` | GREEN |
| doc.go | (none) | GREEN |

**Verified absence**: Zero imports of `runtime/`, `cells/`, or other `adapters/` packages in any production file. Confirmed by running:

```
grep -r "runtime/\|cells/\|adapters/" adapters/rabbitmq/*.go
# (no matches in non-test files)
```

**Dependency direction**: adapters/rabbitmq --> kernel/outbox, kernel/idempotency, pkg/errcode. This is the correct direction (adapters implement kernel-defined interfaces). The only external dependency is `github.com/rabbitmq/amqp091-go`.

**Verdict**: Fully compliant with the layering constraint "adapters/ implements kernel/ or runtime/ defined interfaces".

---

## 3. Interface Implementation Compliance

### 3.1 outbox.Publisher

**Compile-time check**: `adapters/rabbitmq/publisher.go:15`
```go
var _ outbox.Publisher = (*Publisher)(nil)
```

**Kernel interface** (`kernel/outbox/outbox.go:53-55`):
```go
type Publisher interface {
    Publish(ctx context.Context, topic string, payload []byte) error
}
```

**Implementation**: `Publisher.Publish` at publisher.go:31-80 -- correct signature, correct semantics. Uses publisher confirm mode for delivery guarantees. PASS.

### 3.2 outbox.Subscriber

**Compile-time check**: `adapters/rabbitmq/subscriber.go:19`
```go
var _ outbox.Subscriber = (*Subscriber)(nil)
```

**Kernel interface** (`kernel/outbox/outbox.go:63-74`):
```go
type Subscriber interface {
    Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error
    Close() error
}
```

**Implementation**: `Subscriber.Subscribe` at subscriber.go:80-133, `Subscriber.Close` at subscriber.go:221-256 -- correct signatures, correct semantics. PASS.

### 3.3 idempotency.Checker (consumed, not implemented)

ConsumerBase consumes `idempotency.Checker` via dependency injection (consumer_base.go:73-76). The adapter does **not** implement `Checker` -- that is the responsibility of `adapters/redis`. This is architecturally correct: each adapter implements one kernel interface, avoiding cross-adapter coupling.

---

## 4. P0 Findings

### P0-F1: DLQ publish failure silently ACKs original message [Issue #26 CONFIRMED]

**Dimension**: [Consistency Level] -- L2 OutboxFact data loss

**Location**: `adapters/rabbitmq/consumer_base.go:170-173`
```go
// Exhausted all retries -- route to DLQ.
cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount)
// Return nil to ACK the original message (it's been DLQ'd).
return nil
```

And in `deadLetter()` at consumer_base.go:205-213:
```go
if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
    slog.Error("rabbitmq: failed to publish to DLQ",
        slog.String("event_id", entry.ID),
        // ...
    )
    return  // <-- SILENT RETURN, no error propagated
}
```

**Problem**: When `deadLetter()` fails to publish to the DLQ (broker down, DLQ exchange not declared, network error), the method returns without error. The caller (the `Wrap` closure) then returns `nil`, which tells the `Subscriber` to ACK the original message. The message is permanently lost -- neither processed, nor in the DLQ, nor requeued.

**Impact**: HIGH. This violates L2 (OutboxFact) consistency guarantees. Any transient DLQ publish failure causes silent data loss.

**Fix**: `deadLetter()` must return an error. When DLQ publish fails, the `Wrap` closure must return the error (triggering NACK+requeue in Subscriber) rather than ACK. Proposed change:

```go
func (cb *ConsumerBase) deadLetter(...) error {
    // ... marshal + publish ...
    if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
        return fmt.Errorf("dead letter publish to %s: %w", dlqTopic, err)
    }
    return nil
}

// In Wrap():
if err := cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount); err != nil {
    return err  // NACK+requeue, preserving the message
}
return nil  // ACK only after successful DLQ publish
```

**Same pattern applies to the PermanentError DLQ path** at consumer_base.go:141:
```go
cb.deadLetter(ctx, topic, entry, lastErr, attempt+1)
return nil // Return nil to ACK the original message.
```

Both call sites must propagate DLQ publish failure.

---

### P0-F2: Subscriber channel invalidation on reconnect [Issue #25 CONFIRMED]

**Dimension**: [Cell Aggregation / Reconnect Strategy] -- silent consumer death

**Location**: `adapters/rabbitmq/connection.go:213` and `subscriber.go:91-132`

**Problem**: When the AMQP connection drops and `reconnectLoop()` runs:

1. `drainChannelPool()` closes all pooled channels (connection.go:213)
2. A new connection is established (connection.go:220-224)

However, `Subscriber.Subscribe()` holds a **dedicated channel** (subscriber.go:91) that is NOT in the channel pool. It was acquired at subscribe-time and stored in `s.channels` (subscriber.go:96-97). When the underlying AMQP connection is severed:

- The dedicated channel becomes invalid
- The `deliveries` channel returned by `ch.Consume()` (subscriber.go:121) will be closed by the AMQP library
- The `consumeLoop` detects this at subscriber.go:155-159 and returns `ErrAdapterAMQPConsume`
- **But the subscriber does not automatically re-subscribe**. It exits permanently.

The reconnect loop restores the *connection*, but all active subscriptions are dead. The caller receives an error from `Subscribe()` and must manually re-invoke `Subscribe()` -- but there is no mechanism or documentation for this.

**Evidence**: `reconnectLoop()` at connection.go:187-226 only drains the channel pool and re-establishes the connection. It has no callback mechanism to notify subscribers that they need to re-acquire channels and re-consume.

**Impact**: HIGH. After any network blip, all consumers silently stop receiving messages. In production with transient network issues, this leads to complete message processing stall.

**Fix options** (architectural decision required):

1. **Subscriber-level reconnect loop**: Subscriber wraps `consumeLoop` in a retry loop that re-acquires a channel and re-subscribes after channel closure. This is the Watermill pattern.
2. **Connection notification callback**: Connection exposes a `OnReconnect(func())` hook that Subscriber registers to re-setup its channel.
3. **Document-and-delegate**: Document that callers must re-invoke Subscribe in a loop. (Least desirable -- pushes complexity to every consumer.)

Recommendation: Option 1 is the most robust and aligns with the Watermill reference architecture.

---

### P0-F3: PermanentError detection via direct type assertion bypasses wrapping

**Dimension**: [Interface Stability] -- API contract fragility

**Location**: `adapters/rabbitmq/consumer_base.go:134`
```go
if _, ok := lastErr.(*PermanentError); ok {
```

**Problem**: This uses a direct type assertion (`*PermanentError`) instead of `errors.As()`. If a handler wraps a `PermanentError` with `fmt.Errorf("context: %w", NewPermanentError(err))`, the type assertion fails, and the error is treated as transient -- retried up to `RetryCount` times before going to DLQ anyway.

This is a correctness issue because:
1. The `PermanentError` type implements `Unwrap()` (consumer_base.go:57-59), signaling intent to participate in error chains
2. Go error handling best practices require `errors.As()` for type-switched errors
3. Any middleware or handler that wraps errors (which is standard per the project's error-handling.md rule "errors must wrap context") will bypass the permanent error detection

**Evidence**: `PermanentError` has `Unwrap() error` at consumer_base.go:57, but the detection at line 134 does not use the unwrap chain.

**Impact**: HIGH. Any handler following the project's own error wrapping convention (`fmt.Errorf("enrollment: %w", err)`) will cause PermanentErrors to be retried instead of immediately routed to DLQ, wasting retry budget and delaying dead-lettering.

**Fix**:
```go
var permErr *PermanentError
if errors.As(lastErr, &permErr) {
```

---

## 5. P1 Findings

### P1-F1: sanitizeURL leaks AMQP credentials [Prior P0-F12S01]

**Dimension**: [Interface Stability / Security]

**Location**: `adapters/rabbitmq/connection.go:372-379`
```go
func sanitizeURL(url string) string {
    if len(url) > 10 {
        return url[:10] + "***"
    }
    return "***"
}
```

**Problem**: For a typical AMQP URL `amqp://user:password@host:5672/`, the first 10 characters are `amqp://use` which leaks the username prefix. For URLs with short usernames (e.g., `amqp://a:secret@host`), it leaks `amqp://a:s` -- the start of the password.

**Recommendation**: Use `net/url.Parse` to properly redact credentials:
```go
func sanitizeURL(rawURL string) string {
    u, err := url.Parse(rawURL)
    if err != nil {
        return "***"
    }
    u.User = url.UserPassword("***", "***")
    return u.String()
}
```

**Impact**: Medium. Credentials in slog output may reach log aggregation systems.

---

### P1-F2: No TLS configuration support [Prior P1-M5]

**Dimension**: [Extensibility]

**Location**: `adapters/rabbitmq/connection.go:25-44` (Config struct) and `connection.go:110-116` (DefaultDial)

**Problem**: `Config` has no `TLSConfig *tls.Config` field. `DefaultDial` uses `amqp.Dial` which does not support TLS. Production RabbitMQ deployments almost universally require TLS (`amqps://`). The `amqp091-go` library provides `amqp.DialTLS(url, tlsConfig)` for this purpose.

**Recommendation**: Add optional `TLSConfig *tls.Config` to `Config`. In `DefaultDial`, check if TLSConfig is non-nil and use `amqp.DialTLS` accordingly. This is a backward-compatible, additive change.

**Impact**: Medium. Blocks production deployment without TLS.

---

### P1-F3: processDelivery is synchronous, PrefetchCount is underutilized [Prior P1-M7]

**Dimension**: [Performance / Scalability]

**Location**: `adapters/rabbitmq/subscriber.go:161-162`
```go
s.wg.Add(1)
s.processDelivery(ctx, ch, delivery, topic, handler)
```

**Problem**: `processDelivery` is called synchronously in the `consumeLoop` select. Despite setting `Qos(prefetchCount, ...)`, only one message is processed at a time because the loop blocks on each delivery. The prefetch buffer fills up at the broker, but the consumer cannot process them concurrently.

**Recommendation**: Change to `go s.processDelivery(...)` to enable concurrent processing up to `PrefetchCount`. The `sync.WaitGroup` already tracks in-flight messages for graceful shutdown.

**Caveat**: If made concurrent, the `ch.Ack`/`ch.Nack` calls in `processDelivery` need to be thread-safe. The AMQP library's channel operations are NOT concurrency-safe. Two options:
1. Use a dedicated channel per goroutine (expensive)
2. Serialize ACK/NACK through a channel or mutex

This is a design decision that should be made carefully. Track as P1, not P0, because the current serial approach is correct, just suboptimal.

**Impact**: Medium. Throughput limited to 1 message at a time per subscription.

---

### P1-F4: Publisher enables confirm mode on every publish call

**Dimension**: [Performance]

**Location**: `adapters/rabbitmq/publisher.go:43-46`
```go
// Enable confirm mode.
if err := ch.Confirm(false); err != nil {
    return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: enable confirm mode", err)
}
```

**Problem**: `ch.Confirm(false)` is called on every `Publish` invocation. AMQP confirm mode is a per-channel setting -- once enabled, it persists for the channel's lifetime. Re-enabling it on every publish is redundant and adds a round-trip to the broker.

Furthermore, since `AcquireChannel` may return a channel from the pool that already has confirm mode enabled, the redundant `Confirm` call is a no-op but still incurs network overhead.

**Recommendation**: Enable confirm mode once when the channel is created (in `AcquireChannel` or via a confirm-aware channel wrapper), not on every publish.

**Impact**: Medium. Adds one unnecessary AMQP round-trip per publish.

---

## 6. P2 Findings

### P2-F1: Published messages lack AMQP MessageId [Prior P1-M6]

**Dimension**: [Observability / Traceability]

**Location**: `adapters/rabbitmq/publisher.go:50-55`
```go
msg := amqp.Publishing{
    ContentType:  "application/octet-stream",
    DeliveryMode: amqp.Persistent,
    Timestamp:    time.Now().UTC(),
    Body:         payload,
}
```

**Problem**: The `amqp.Publishing` does not set `MessageId`. Without a MessageId, broker-level tracing (RabbitMQ Management UI, Firehose, Shovel) cannot correlate messages. The outbox `Entry.ID` is the natural candidate for `MessageId`.

**Recommendation**: The publisher receives raw `[]byte` payload (not an `Entry`), so it cannot extract `Entry.ID` without unmarshalling. Two options:
1. Add an optional `MessageId string` parameter to `Publish` (breaking interface change -- avoid)
2. Set `MessageId` to a UUID generated per-publish (provides broker traceability but not domain correlation)
3. Accept the Entry.ID as a header via the AMQP `Headers` table (best option: relay can set `x-event-id` header)

**Impact**: Low. Mainly affects debugging and monitoring.

---

### P2-F2: No metrics or tracing hooks [Prior P1-M4]

**Dimension**: [Observability]

**Location**: All files.

**Problem**: DLQ routing, publish confirmations, reconnect events, and consumer processing are all logged via `slog` but have no metrics counters or OpenTelemetry span integration. The observability spec (`.claude/rules/gocell/observability.md`) and the `runtime/observability` package exist, but the adapter does not integrate with them.

Key metrics missing:
- `rabbitmq_messages_published_total` (with topic label)
- `rabbitmq_messages_consumed_total` (with topic, status=ack/nack/dlq labels)
- `rabbitmq_dlq_routed_total` (with topic, consumer_group labels)
- `rabbitmq_reconnect_total`
- `rabbitmq_publish_confirm_latency_seconds`

**Impact**: Low. The adapter functions correctly; observability is a production-readiness concern.

---

### P2-F3: ConsumerBase retry blocks the Subscriber goroutine

**Dimension**: [Performance]

**Location**: `adapters/rabbitmq/consumer_base.go:155-159`
```go
select {
case <-time.After(delay):
case <-ctx.Done():
    return ctx.Err()
}
```

**Problem**: The `Wrap` closure performs retry with backoff using `time.After` delays. Since `processDelivery` calls the handler synchronously (see P1-F3), the retry backoff blocks the entire consume loop. If `RetryCount=3` and `RetryBaseDelay=1s`, a failing message blocks all other messages on that subscription for up to 1s + 2s + 4s = 7 seconds.

**Impact**: Low (given P1-F3 serial processing, this is a secondary concern). If P1-F3 is fixed to enable concurrent processing, this becomes moot per message but still wastes a goroutine during backoff.

---

## 7. TryProcess / Issue #18 Verification

The TOCTOU fix is correctly implemented. Evidence:

**consumer_base.go:107**:
```go
shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
```

This replaces the old two-step `IsProcessed` + `MarkProcessed` pattern with a single atomic call. The `TryProcess` method was added to the `idempotency.Checker` interface (`kernel/idempotency/idempotency.go:23-27`) and the mock implementation in tests correctly simulates atomic check-and-mark semantics (`rabbitmq_test.go:239-250`).

**Architect verdict**: Issue #18 fix is **ACCEPTED**. The atomic TryProcess eliminates the TOCTOU race. The fail-open behavior (line 113: `shouldProcess = true` when TryProcess errors) is a reasonable design choice for availability, with appropriate warning-level logging.

---

## 8. Publisher/Subscriber Decoupling Assessment

**Can Publisher and Subscriber be used independently?** YES.

- `Publisher` depends only on `*Connection` (publisher.go:18-20)
- `Subscriber` depends only on `*Connection` (subscriber.go:49-50)
- `ConsumerBase` depends on `idempotency.Checker` and `outbox.Publisher` (consumer_base.go:73-76) -- it does NOT depend on `Subscriber`
- `Connection` is shared infrastructure but does not force publisher or subscriber creation

A caller can create `NewConnection(...)` + `NewPublisher(conn)` without ever creating a Subscriber, and vice versa. This is correct for the adapter pattern -- producers and consumers may live in different assemblies.

**ConsumerBase** is also independently usable. It wraps any `func(context.Context, outbox.Entry) error` handler and can be combined with any `outbox.Subscriber` implementation, not just the RabbitMQ one. This is good design.

---

## 9. DLQ Architecture Assessment

### Routing Strategy

DLQ topic defaults to `{topic}.dlq` (consumer_base.go:181-183), configurable via `ConsumerBaseConfig.DLQTopic`. Messages are enriched with `x-death-*` metadata (consumer_base.go:190-194):
- `x-death-reason`: original error message
- `x-death-topic`: source topic
- `x-death-consumer-group`: consumer group identifier
- `x-death-retry-count`: number of attempts
- `x-death-time`: ISO 8601 timestamp

This metadata is sufficient for manual inspection and automated reprocessing.

### Reflow Mechanism

**No reflow mechanism exists.** There is no DLQ consumer, no replay tool, and no API to move messages from DLQ back to the source topic. This is acceptable for v1.0 -- DLQ reflow is typically an operational tool, not a framework feature. However, it should be tracked as a post-v1.0 item.

### DLQ Exchange Declaration

The DLQ topic exchange is declared implicitly by `Publisher.Publish` (publisher.go:39) when `deadLetter()` calls `publisher.Publish(ctx, dlqTopic, payload)`. This means the DLQ exchange is created on first failure, which is correct for the fanout exchange pattern used here. A DLQ subscriber must bind to this exchange to receive dead-lettered messages.

---

## 10. Reconnect Strategy Assessment

### Connection Level

The reconnect loop (connection.go:187-226) is well-designed:
- Exponential backoff with configurable base delay and max cap (connection.go:262-268)
- Graceful shutdown via `closeCh` (connection.go:232-234)
- Channel pool drain on disconnect (connection.go:213)
- `WaitConnected()` for callers to block until reconnection (connection.go:358-369)
- `connected` channel re-creation for reconnect signaling (connection.go:216-218, 222-224)

### Channel Pool Recovery

The channel pool is drained on disconnect (connection.go:213 calls `drainChannelPool()`). After reconnection, `AcquireChannel()` creates fresh channels from the new connection. This is correct -- pooled channels from the old connection would be invalid.

### Subscriber Recovery (P0-F2)

As detailed in P0-F2, subscriber-held channels are NOT recovered. This is the primary gap in the reconnect strategy.

---

## 11. Architectural Recommendations Summary

```
1. [Consistency Level] P0-F1: deadLetter must return error; Wrap must propagate DLQ
   publish failure as NACK+requeue -- Reason: Silent data loss on DLQ failure
   violates L2 OutboxFact guarantees -- Impact: HIGH

2. [Reconnect Strategy] P0-F2: Subscriber must implement reconnect-aware consume
   loop that re-acquires channel and re-subscribes after connection loss
   -- Reason: All consumers silently die after any network disruption
   -- Impact: HIGH

3. [Interface Stability] P0-F3: Replace direct type assertion `lastErr.(*PermanentError)`
   with `errors.As(lastErr, &permErr)` -- Reason: Wrapped PermanentErrors bypass
   DLQ routing, violating the documented API contract -- Impact: HIGH

4. [Security] P1-F1: Replace fixed-offset sanitizeURL with net/url.Parse credential
   redaction -- Reason: Current implementation leaks username and potentially
   password prefix to log aggregation -- Impact: MEDIUM

5. [Extensibility] P1-F2: Add optional TLSConfig to Config struct
   -- Reason: Production RabbitMQ requires TLS; current adapter cannot connect
   to amqps:// endpoints -- Impact: MEDIUM

6. [Performance] P1-F3: Consider async processDelivery with channel-safe ACK/NACK
   -- Reason: Serial processing underutilizes PrefetchCount, limiting throughput
   -- Impact: MEDIUM

7. [Performance] P1-F4: Move confirm mode enablement from per-publish to
   per-channel-creation -- Reason: Redundant broker round-trip on every publish
   -- Impact: MEDIUM

8. [Observability] P2-F1: Set AMQP MessageId on published messages
   -- Reason: Enables broker-level tracing and correlation -- Impact: LOW

9. [Observability] P2-F2: Add metrics counters for publish/consume/DLQ/reconnect
   -- Reason: Production observability requirement -- Impact: LOW

10. [Performance] P2-F3: ConsumerBase retry backoff blocks consume loop
    -- Reason: Failing messages stall all other messages on the subscription
    -- Impact: LOW
```

---

## 12. Architectural Verdict on Prior Findings

| Finding ID | Original Severity | Verdict | Rationale |
|-----------|-------------------|---------|-----------|
| Issue #18 (TOCTOU) | P0 | **ACCEPTED as RESOLVED** | TryProcess atomic method correctly eliminates the race |
| Issue #25 (reconnect subscriber) | P0 | **ACCEPTED as P0** | Subscribers do not recover after reconnect; confirmed in code |
| Issue #26 (DLQ publish + ACK) | P0 | **ACCEPTED as P0** | deadLetter() silently swallows publish errors; confirmed in code |
| P0-F12S01 (sanitizeURL) | P0 | **DOWNGRADED to P1** | Credential leak is real but limited (10-char prefix in structured logs); net/url.Parse fix is straightforward |
| P0-F12D01 (ctx cancel requeue) | P0 | **DOWNGRADED to P1** | ctx cancellation returning error -> NACK+requeue is by-design in the Subscriber (transient). The ConsumerBase Wrap handles ctx.Done by returning ctx.Err() which triggers NACK+requeue -- appropriate for graceful shutdown. Not a data loss vector. |
| P1-M7 (serial processDelivery) | P1 | **ACCEPTED as P1** | Correct behavior but suboptimal performance |
| P1-M5 (no TLS) | P1 | **ACCEPTED as P1** | Blocks production deployment |
| P1-M6 (no MessageId) | P1 | **DOWNGRADED to P2** | Observability nicety, not a correctness issue |
| P1-L8 (reconnect test coverage) | P1 | **ACCEPTED as P1** | reconnectLoop/reconnectWithBackoff have zero unit test coverage |
| P1-L9 (concurrent consume coverage) | P1 | **ACCEPTED as P1** | No concurrent consumer tests exist |

---

## 13. Dependency Compliance Matrix

| Package | stdlib | pkg/ | kernel/ | runtime/ | adapters/ | cells/ | Verdict |
|---------|--------|------|---------|----------|-----------|--------|---------|
| adapters/rabbitmq | context, encoding/json, fmt, log/slog, math, sync, sync/atomic, time | errcode | outbox, idempotency | -- | -- | -- | **GREEN** |

---

## 14. Dimension Scores

| Dimension | Score | Evidence |
|-----------|-------|----------|
| Layer compliance | **GREEN** | Only depends on kernel/ + pkg/ + stdlib + amqp091-go |
| Cell aggregation boundary | **GREEN** | Adapter is self-contained; no cross-adapter coupling |
| Interface stability | **YELLOW** | PermanentError type assertion (P0-F3) bypasses error chain; Publisher interface adequate |
| Consistency level | **RED** | DLQ publish failure silently ACKs (P0-F1); subscriber death on reconnect (P0-F2) |
| Performance / scalability | **YELLOW** | Serial processDelivery (P1-F3); per-publish confirm mode (P1-F4) |
| Dependency direction | **GREEN** | No reverse dependencies confirmed |
