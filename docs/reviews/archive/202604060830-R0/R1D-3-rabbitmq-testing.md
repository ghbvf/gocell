# R1D-3: adapters/rabbitmq Testing Review

| Field | Value |
|---|---|
| Reviewer Seat | S3 Test / QA |
| Scope | `adapters/rabbitmq/` unit + integration tests |
| Review basis commit | `5096d4f` |
| Date | 2026-04-06 |
| Verification mode | Unit tests executed via `/opt/homebrew/bin/go`; integration-tag suite not executed |

---

## Summary

The module has broad unit-test coverage and real-broker integration tests, but the highest-risk failure paths are still unproven. In particular, the test suite does not protect the two delivery-safety regressions currently visible in code: DLQ publish failure still ACKs the original message, and shutdown/requeue behavior is not tested end to end.

Observed local verification:
- `/opt/homebrew/bin/go test ./adapters/rabbitmq/...` -> pass
- `/opt/homebrew/bin/go test -cover ./adapters/rabbitmq/...` -> `78.7%` statement coverage
- `-tags integration` not run in this review window

**Verdict**: `BLOCKED`

| Severity | Count | Notes |
|---|---|---|
| P0 | 2 | Message-loss and shutdown semantics are not covered |
| P1 | 2 | Reconnect and subscribe-setup failures are largely untested |
| P2 | 1 | Several tests overclaim coverage and rely on fixed sleeps |

---

## Findings

### F-01 [P0] No test proves DLQ publish failure preserves the original message

**Files**:
- `adapters/rabbitmq/consumer_base.go:163-173`
- `adapters/rabbitmq/consumer_base.go:205-212`
- `adapters/rabbitmq/rabbitmq_test.go:940-1002`
- `adapters/rabbitmq/integration_test.go:147-234`

**Evidence**:

`Wrap()` always returns `nil` after calling `deadLetter()`:

```go
cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount)
return nil
```

But `deadLetter()` swallows publish failure:

```go
if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
    slog.Error("rabbitmq: failed to publish to DLQ", ...)
    return
}
```

Current tests only cover the success path where DLQ publish succeeds. There is no unit or integration case where `publisher.Publish()` fails and the caller must decide whether to ACK or NACK the original delivery.

**Why it matters**:
This is a silent message-loss path. If DLQ publish fails and the wrapper still returns `nil`, `Subscriber.processDelivery()` will ACK the original RabbitMQ delivery.

**Recommendation**:
Add one unit test with a failing mock publisher and one integration-style test that asserts the wrapper returns non-nil when DLQ publish fails, so the original message is not ACKed.

**Status**: Confirmed historical plan item `#26`

---

### F-02 [P0] No end-to-end test covers `ConsumerBase + Subscriber` shutdown/cancel interaction

**Files**:
- `adapters/rabbitmq/consumer_base.go:155-159`
- `adapters/rabbitmq/subscriber.go:197-208`
- `adapters/rabbitmq/rabbitmq_test.go:741-781`
- `adapters/rabbitmq/rabbitmq_test.go:1051-1071`

**Evidence**:

`ConsumerBase.Wrap()` returns `ctx.Err()` when retry backoff is interrupted:

```go
case <-ctx.Done():
    return ctx.Err()
```

`Subscriber.processDelivery()` treats any handler error as transient and requeues:

```go
if err := handler(ctx, entry); err != nil {
    if nackErr := ch.Nack(delivery.DeliveryTag, false, true); nackErr != nil {
        ...
    }
    return
}
```

The current suite tests these behaviors separately, but not together. There is no end-to-end case that invokes `Subscriber` with a wrapped `ConsumerBase` handler and then cancels the context during backoff.

**Why it matters**:
This is the exact path where shutdown safety, duplicate delivery, and idempotency semantics interact. Without an end-to-end test, the suite cannot prove the adapter behaves correctly during real shutdown.

**Recommendation**:
Add an integration-style unit test that wires `Subscriber.processDelivery()` to a `ConsumerBase.Wrap()` handler, cancels context during retry backoff, and asserts the final ACK/NACK decision explicitly.

**Status**: Confirms the earlier `ctx cancel -> NACK+requeue` risk remains unverified

---

### F-03 [P1] Reconnect logic is barely tested

**Files**:
- `adapters/rabbitmq/connection.go:187-259`
- `adapters/rabbitmq/rabbitmq_test.go:315-438`
- `adapters/rabbitmq/integration_test.go:236-250`

**Evidence**:

`reconnectLoop()` and `reconnectWithBackoff()` are the core reliability path, but the current tests do not simulate:
- a `NotifyClose` event
- repeated dial failures
- `WaitConnected()` during disconnect/reconnect
- a live subscriber or publisher surviving a reconnect

The so-called `TestIntegration_ConnectionRecovery` only checks `Health()` and a channel acquire/release cycle. It never forces a disconnect.

**Why it matters**:
Reconnect is one of the module's core promises. An untested reconnect path is effectively unsupported in production.

**Recommendation**:
Add mock-dial tests for disconnect and re-dial behavior, then add one real-broker recovery test that forces a disconnect and verifies publish/subscribe resumes afterward.

**Status**: Confirmed historical reconnect test gap

---

### F-04 [P1] Subscribe setup failure matrix is uncovered

**Files**:
- `adapters/rabbitmq/subscriber.go:90-123`
- `adapters/rabbitmq/rabbitmq_test.go:639-848`

**Evidence**:

After `AcquireChannel()`, `Subscribe()` can fail at five setup steps:
- `Qos`
- `ExchangeDeclare`
- `QueueDeclare`
- `QueueBind`
- `Consume`

The current tests cover success, unmarshal failure, handler failure, default queue name, closed subscriber, and closed delivery channel, but none of those setup failures.

**Why it matters**:
Consumer bootstrap failures are the path most likely to leak channels, return the wrong error code, or leave the subscriber in a partially initialized state.

**Recommendation**:
Extend `mockChannel` with failure injection for each setup call and add table-driven tests for all five branches.

**Status**: New

---

### F-05 [P2] Some tests overstate their coverage and rely on fixed sleeps

**Files**:
- `adapters/rabbitmq/integration_test.go:147-234`
- `adapters/rabbitmq/integration_test.go:183-184`
- `adapters/rabbitmq/rabbitmq_test.go:675-677`
- `adapters/rabbitmq/rabbitmq_test.go:726-727`
- `adapters/rabbitmq/rabbitmq_test.go:769-770`

**Evidence**:

`TestIntegration_ConsumerBaseRetry` claims to validate retry + DLQ with real infrastructure, but it calls the wrapped handler directly and bypasses the RabbitMQ ACK/NACK path entirely. Several tests also rely on `time.Sleep(50ms)` / `time.Sleep(500ms)` for synchronization.

**Why it matters**:
These tests create a false sense of safety around the hardest delivery paths and may become flaky under load.

**Recommendation**:
Rename tests to match their real scope or extend them to cover the broker-driven path, and replace fixed sleeps with ready channels or polling assertions.

**Status**: New
