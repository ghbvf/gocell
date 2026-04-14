# R1D-3: adapters/rabbitmq Security Review

- **Reviewer Seat**: S2 (Security / Permissions)
- **Scope**: `adapters/rabbitmq/` all .go files (~950 LOC on develop)
- **Review Baseline**: commit `ce03ba1` (develop HEAD)
- **Date**: 2026-04-06

---

## Summary

The `adapters/rabbitmq` package implements `outbox.Publisher` and `outbox.Subscriber` with auto-reconnect, channel pooling, publisher confirm mode, and a `ConsumerBase` providing idempotency + retry + DLQ routing. The security review focused on six core concerns raised from prior PR#7-12 reviews. **3 P0 findings** remain open or partially mitigated, **3 P1 findings** identified, and **2 P2 findings** noted.

---

## Findings

### P0-R1D3-01: sanitizeURL Still Leaks AMQP Credentials (CONFIRMED -- NOT FIXED)

- **Seat**: S2 Security
- **Severity**: P0
- **Category**: Credential Leakage
- **Affected File**: `adapters/rabbitmq/connection.go` lines 372-379
- **Prior Finding**: P0-F12S01

**Evidence**:

```go
// connection.go:372-379
func sanitizeURL(url string) string {
    // Simple approach: just indicate the host portion.
    // In production, parse the URL and redact credentials.
    if len(url) > 10 {
        return url[:10] + "***"
    }
    return "***"
}
```

The function truncates the URL to its first 10 characters. For a typical AMQP URL like `amqp://admin:SuperSecret@broker.internal:5672/vhost`, the first 10 characters are `amqp://adm`, which leaks the username prefix. For short usernames (e.g., `amqp://a:b@host`), significant credential material is exposed.

The unit test at `rabbitmq_test.go:478` confirms the broken behavior:
```go
{name: "long URL", url: "amqp://guest:guest@localhost:5672/", expected: "amqp://gue***"},
```
The test shows `amqp://gue***` as the expected output -- meaning 3 characters of the username `guest` are leaked. The test codifies the vulnerability rather than catching it.

The code's own comment says `"In production, parse the URL and redact credentials"` -- this was never implemented.

**Callers**: `connect()` at line 183 logs the result via `slog.Info`, meaning credentials leak to structured logs in every connection establishment and every reconnect.

**Fix Recommendation**: Replace with proper URL parsing:

```go
import "net/url"

func sanitizeURL(rawURL string) string {
    u, err := url.Parse(rawURL)
    if err != nil {
        return "***"
    }
    u.User = nil
    return u.String()
}
```

**Status**: OPEN

---

### P0-R1D3-02: DLQ Publish Failure Silently ACKs Original Message (Issue #26)

- **Seat**: S2 Security
- **Severity**: P0
- **Category**: Message Loss / Data Integrity
- **Affected File**: `adapters/rabbitmq/consumer_base.go` lines 170-174, 196-213
- **Prior Finding**: Issue #26

**Evidence**:

In `ConsumerBase.Wrap()`, after retry exhaustion (line 170-174):
```go
// consumer_base.go:170-174
cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount)

// Return nil to ACK the original message (it's been DLQ'd).
return nil
```

But inside `deadLetter()` (lines 205-212):
```go
// consumer_base.go:205-212
if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
    slog.Error("rabbitmq: failed to publish to DLQ",
        slog.String("event_id", entry.ID),
        ...
    return  // <-- returns without any indication of failure
}
```

When `cb.publisher.Publish()` fails, `deadLetter()` returns (void), and then `Wrap()` returns `nil`, causing the Subscriber to ACK the original delivery. The message is permanently lost: it was not successfully processed, not in the DLQ, and not requeued.

The same pattern applies to the permanent error path (line 141):
```go
// consumer_base.go:139-141
cb.deadLetter(ctx, topic, entry, lastErr, attempt+1)
return nil // Return nil to ACK the original message.
```

And the marshal failure path (lines 196-203):
```go
// consumer_base.go:196-203
payload, err := json.Marshal(dlqEntry)
if err != nil {
    slog.Error("rabbitmq: failed to marshal DLQ entry", ...)
    return  // <-- silent return, caller ACKs
}
```

**Impact**: Any DLQ publish failure (broker down, network partition, exchange not declared) causes silent, permanent message loss. This is a data integrity violation for L2+ consistency levels.

**Fix Recommendation**: `deadLetter()` must return an `error`. When it fails, `Wrap()` must return a non-nil error so the Subscriber NACKs the original message (with requeue). Example:

```go
func (cb *ConsumerBase) deadLetter(...) error {
    // ...
    if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
        slog.Error(...)
        return fmt.Errorf("deadLetter publish: %w", err)
    }
    return nil
}

// In Wrap():
if err := cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount); err != nil {
    return err  // Non-nil -> Subscriber NACKs with requeue
}
return nil
```

**Status**: OPEN

---

### P0-R1D3-03: Context Cancellation During Retry Causes Uncontrolled Requeue

- **Seat**: S2 Security
- **Severity**: P0
- **Category**: Message Safety / Requeue Storm
- **Affected File**: `adapters/rabbitmq/consumer_base.go` lines 155-159, `adapters/rabbitmq/subscriber.go` lines 197-208
- **Prior Finding**: P0-F12D01

**Evidence**:

In `ConsumerBase.Wrap()` retry backoff (lines 155-159):
```go
// consumer_base.go:155-159
select {
case <-time.After(delay):
case <-ctx.Done():
    return ctx.Err()
}
```

When context is cancelled during retry backoff, `Wrap()` returns `ctx.Err()` (non-nil). This flows to `Subscriber.processDelivery()` line 197:

```go
// subscriber.go:197-208
if err := handler(ctx, entry); err != nil {
    // Handler error is a transient failure -- NACK with requeue.
    slog.Warn("rabbitmq: handler returned error, nacking with requeue", ...)
    if nackErr := ch.Nack(delivery.DeliveryTag, false, true); nackErr != nil {
        ...
    }
    return
}
```

The Subscriber treats ALL handler errors as transient and NACKs with `requeue=true`. So context cancellation (shutdown) causes every in-flight message to be requeued. On restart, these messages are re-delivered, potentially hitting the same ctx cancellation and requeuing again -- creating a requeue storm.

The test at `rabbitmq_test.go:1051-1071` (`TestConsumerBase_Wrap_ContextCancelled_DuringRetry`) verifies that `ctx.Err()` is returned, but no test validates the Subscriber's NACK behavior on ctx cancellation.

**Fix Recommendation**: The Subscriber should distinguish context cancellation from handler errors:

```go
func (s *Subscriber) processDelivery(...) {
    defer s.wg.Done()
    // ...
    if err := handler(ctx, entry); err != nil {
        if ctx.Err() != nil {
            // Context cancelled (shutdown) -- NACK without requeue.
            // Message will be re-delivered when consumer reconnects normally.
            ch.Nack(delivery.DeliveryTag, false, false)
            return
        }
        // Transient error -- NACK with requeue.
        ch.Nack(delivery.DeliveryTag, false, true)
        return
    }
    // ...
}
```

Alternatively, consider routing to DLQ instead of requeue on shutdown, depending on business requirements.

**Status**: OPEN

---

### P1-R1D3-04: No TLS Support -- Credentials and Messages Transmitted in Plaintext

- **Seat**: S2 Security
- **Severity**: P1
- **Category**: Transport Security
- **Affected Files**: `adapters/rabbitmq/connection.go` lines 25-44, 110-116

**Evidence**:

The `Config` struct (lines 25-44) has no TLS configuration field:
```go
type Config struct {
    URL string
    ReconnectMaxBackoff time.Duration
    ReconnectBaseDelay  time.Duration
    ChannelPoolSize     int
    ConfirmTimeout      time.Duration
}
```

The `DefaultDial` function (lines 110-116) uses `amqp.Dial(url)` which does NOT support TLS:
```go
func DefaultDial(url string) (AMQPConnection, error) {
    conn, err := amqp.Dial(url)
    // ...
}
```

The `amqp091-go` library provides `amqp.DialTLS(url, tlsConfig)` for TLS connections, but the adapter has no mechanism to use it. Users can work around this via `WithDialFunc()`, but there is no documented guidance, no `TLSConfig` field, and no validation that production URLs use `amqps://`.

**Impact**: AMQP credentials (in the URL) and all message payloads are transmitted in plaintext. In any non-localhost deployment, this is a significant security risk.

**Fix Recommendation**:
1. Add a `TLSConfig *tls.Config` field to `Config`.
2. When `TLSConfig` is non-nil, use `amqp.DialTLS()` in `DefaultDial`.
3. Log a warning if the URL scheme is `amqp://` (not `amqps://`) and `TLSConfig` is nil.
4. Document the TLS configuration in godoc.

**Status**: OPEN

---

### P1-R1D3-05: TryProcess Fail-Open Risks Duplicate Processing

- **Seat**: S2 Security
- **Severity**: P1
- **Category**: Data Integrity / Idempotency
- **Affected File**: `adapters/rabbitmq/consumer_base.go` lines 107-115

**Evidence**:

```go
// consumer_base.go:107-115
shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
if err != nil {
    slog.Warn("rabbitmq: idempotency check failed, proceeding with handler",
        slog.String("event_id", entry.ID),
        ...
    // On error, default to processing (fail-open) to avoid dropping messages.
    shouldProcess = true
}
```

When the idempotency store (e.g., Redis) is unavailable, the system falls back to processing every message. This is a deliberate fail-open design. The rationale ("avoid dropping messages") is valid for availability, but it means:

1. During Redis outage, ALL messages are processed regardless of deduplication. If the same message is delivered N times (e.g., due to requeue or consumer rebalancing), it will be processed N times.
2. There is no rate limiting, circuit breaker, or metric emission on this fallback path. A sustained Redis outage will cause silent mass duplication with only a `Warn`-level log.
3. For non-idempotent handlers (side effects like sending emails, charging payments), this can cause real damage.

The test at `rabbitmq_test.go:1030-1049` confirms this behavior explicitly.

**Fix Recommendation**:
1. Emit a metric counter (e.g., `rabbitmq_idempotency_fallback_total`) so monitoring can alert on degraded dedup.
2. Document clearly in godoc that handlers wrapped by `ConsumerBase` MUST be idempotent at the business level, because the infrastructure-level dedup is best-effort.
3. Consider adding a `FailClosed bool` config option for critical handlers that should NACK when idempotency is unavailable.

**Status**: OPEN

---

### P1-R1D3-06: Reconnect Leaves Stale Channel References in Subscriber

- **Seat**: S2 Security
- **Severity**: P1
- **Category**: Message Safety / Reconnect
- **Affected Files**: `adapters/rabbitmq/subscriber.go` lines 90-132, `adapters/rabbitmq/connection.go` lines 187-226

**Evidence**:

When the AMQP connection drops, `Connection.reconnectLoop()` (connection.go:213) calls `drainChannelPool()` which only drains the pool. However, channels already acquired by `Subscriber.Subscribe()` (stored in `s.channels` at line 96-97) are NOT invalidated.

```go
// subscriber.go:90-97
ch, err := s.conn.AcquireChannel()
if err != nil {
    return errcode.Wrap(...)
}
s.mu.Lock()
s.channels = append(s.channels, ch)
s.mu.Unlock()
```

When the connection drops, the AMQP library closes the delivery channel (the `<-chan amqp.Delivery` returned by `ch.Consume`), which causes `consumeLoop` to exit with an error at line 156-159:

```go
// subscriber.go:154-159
case delivery, ok := <-deliveries:
    if !ok {
        slog.Warn("rabbitmq: delivery channel closed, subscriber exiting", ...)
        return errcode.New(ErrAdapterAMQPConsume, "rabbitmq: delivery channel closed")
    }
```

The subscriber exits but does NOT re-subscribe. The caller (the goroutine that called `Subscribe`) must handle the reconnect. There is no built-in reconnect loop in the Subscriber. Any messages published between the connection drop and the caller re-subscribing are lost from this consumer's perspective (they accumulate in the queue but are not consumed).

Furthermore, there is a window between reconnect and the delivery channel closing where the old channel may have outstanding unacknowledged deliveries. ACK/NACK on these deliveries will fail because the underlying AMQP channel is closed, but the errors are only logged (subscriber.go:213, 203, 186).

**Fix Recommendation**:
1. Document clearly that `Subscribe()` exits on connection loss and callers must implement a reconnect loop (or add a reconnect loop to Subscriber).
2. Consider tracking the connection generation/epoch to detect stale channels.

**Status**: OPEN

---

### P2-R1D3-07: Pooled Channels Re-enter Confirm Mode on Every Publish

- **Seat**: S2 Security
- **Severity**: P2
- **Category**: Correctness / Performance
- **Affected File**: `adapters/rabbitmq/publisher.go` lines 43-48

**Evidence**:

```go
// publisher.go:43-48
// Enable confirm mode.
if err := ch.Confirm(false); err != nil {
    return errcode.Wrap(ErrAdapterAMQPPublish, "rabbitmq: enable confirm mode", err)
}
confirmCh := ch.NotifyPublish(make(chan amqp.Confirmation, 1))
```

Every `Publish()` call acquires a channel from the pool and calls `ch.Confirm(false)` again. Per the AMQP spec, calling `confirm` on an already-confirmed channel is a no-op (the method is idempotent), but `NotifyPublish` creates a new notification channel each time. Previous notification channels from earlier publishes on the same pooled channel are orphaned, leaking goroutines if any are blocked.

**Fix Recommendation**: Either mark channels as confirmed when first created (and skip re-confirmation), or do not pool channels used for confirms.

**Status**: OPEN

---

### P2-R1D3-08: No Input Validation on Topic/QueueName Parameters

- **Seat**: S2 Security
- **Severity**: P2
- **Category**: Input Validation
- **Affected Files**: `adapters/rabbitmq/publisher.go` line 31, `adapters/rabbitmq/subscriber.go` line 80

**Evidence**:

Both `Publisher.Publish()` and `Subscriber.Subscribe()` accept arbitrary `topic` strings that are used directly as AMQP exchange names:

```go
// publisher.go:39
ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil)

// subscriber.go:105
ch.ExchangeDeclare(topic, "fanout", true, false, false, false, nil)
```

There is no validation on the topic string. While RabbitMQ itself rejects invalid exchange names (max 255 bytes, no null characters), passing empty strings or strings with special AMQP characters could cause confusing errors. More importantly, if topic values are derived from user input anywhere in the call chain, this could allow an attacker to declare arbitrary exchanges on the broker.

**Fix Recommendation**: Add basic validation (non-empty, max length, alphanumeric + dots/hyphens/underscores) on topic and queue name inputs.

**Status**: OPEN

---

## GoCell Layering Check (S2 cross-check)

| Check | Result |
|-------|--------|
| kernel/ imports runtime/adapters/cells? | NO -- kernel/idempotency and kernel/outbox have no such imports |
| cells/ imports adapters/? | N/A -- adapters/rabbitmq has no cells/ dependency |
| adapters/rabbitmq imports | `kernel/idempotency`, `kernel/outbox`, `pkg/errcode` -- CORRECT direction |
| Cross-Cell import? | N/A |
| CUD operations have consistency level? | NOT ANNOTATED -- consumer_base.go handles L2 events but no consistency level annotation in code |

---

## Prior Finding Resolution Status

| Prior Finding ID | Description | Status in This Review |
|------------------|-------------|----------------------|
| P0-F12S01 | sanitizeURL fixed truncation leaks AMQP credentials | **CONFIRMED NOT FIXED** -- see P0-R1D3-01 |
| P0-F12D01 | ctx cancel causes NACK+requeue on shutdown | **CONFIRMED NOT FIXED** -- see P0-R1D3-03 |
| Issue #26 | DLQ publish failure silently ACKs original message | **CONFIRMED NOT FIXED** -- see P0-R1D3-02 |

---

## Findings Summary

| ID | Severity | Category | File | Status |
|----|----------|----------|------|--------|
| P0-R1D3-01 | P0 | Credential Leakage | connection.go:372-379 | OPEN |
| P0-R1D3-02 | P0 | Message Loss (DLQ fail -> ACK) | consumer_base.go:170-174,205-212 | OPEN |
| P0-R1D3-03 | P0 | Requeue Storm on ctx cancel | consumer_base.go:155-159, subscriber.go:197-208 | OPEN |
| P1-R1D3-04 | P1 | No TLS Support | connection.go:25-44,110-116 | OPEN |
| P1-R1D3-05 | P1 | Fail-Open Idempotency | consumer_base.go:107-115 | OPEN |
| P1-R1D3-06 | P1 | Stale Channels on Reconnect | subscriber.go:90-132, connection.go:187-226 | OPEN |
| P2-R1D3-07 | P2 | Repeated Confirm Mode | publisher.go:43-48 | OPEN |
| P2-R1D3-08 | P2 | No Topic/Queue Validation | publisher.go:31, subscriber.go:80 | OPEN |

**Verdict**: 3 P0 findings block merge for any security-sensitive deployment. P0-R1D3-02 (DLQ publish failure -> silent ACK) is the most critical as it causes permanent, silent message loss.
