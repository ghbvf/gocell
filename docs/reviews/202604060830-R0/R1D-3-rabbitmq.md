# R1D-3: adapters/rabbitmq Six-Role Review

| Field | Value |
|---|---|
| Reviewer Seats | S1 Architecture, S2 Security, S3 Test/QA, S4 DevOps/SRE, S5 DX/Maintainability, S6 Product/Delivery |
| Scope | `src/adapters/rabbitmq/` (`connection.go`, `publisher.go`, `subscriber.go`, `consumer_base.go`) |
| Review basis commit | `5096d4f` |
| Date | 2026-04-06 |
| Verification | `/opt/homebrew/bin/go test ./adapters/rabbitmq/...` from `src/` -> PASS |

---

## Executive Summary

This module still blocks Round 1D. The six-role review confirms **4 historical P0s** and surfaces **1 new P0** around the default subscription path. The core pattern is consistent: the code advertises `DLQ + retry + idempotency + reconnect`, but the default runtime wiring only delivers a subset of that contract.

| Severity | Count | Summary |
|---|---:|---|
| P0 | 5 | DLQ semantics not wired by default; DLQ publish failure ACKs original; reconnect does not restore subscribers; ConsumerBase cancel/idempotency semantics still unsafe; `sanitizeURL` still leaks credential fragments |
| P1 | 0 | -- |
| P2 | 0 | -- |

**Round verdict**: `BLOCKED`

---

## Notable Improvements Since PR #12 Baseline

1. The old `IsProcessed + MarkProcessed` TOCTOU race is no longer present at interface level: `kernel/idempotency.Checker` now exposes `TryProcess(...)`, and `adapters/redis` implements it atomically with `SET NX`.
2. `src/adapters/rabbitmq/` is no longer "zero-test": unit tests pass locally, and an `integration_test.go` file now exists. However, the missing tests are exactly the critical ones: reconnect recovery, DLQ publish failure, and cancel/redelivery semantics.

---

## F-01: Default subscription path does not implement the promised DLQ / retry / idempotency semantics

| Field | Value |
|---|---|
| Seats | S1 Architecture + S5 DX + S6 Product/Delivery |
| Severity | **P0** |
| Category | Delivery Semantics / Unsafe Default |
| Files | `src/kernel/outbox/outbox.go:64-70`, `src/adapters/rabbitmq/subscriber.go:109-121`, `src/adapters/rabbitmq/subscriber.go:176-208`, `src/runtime/bootstrap/bootstrap.go:265-273`, `src/cells/config-core/cell.go:176-183`, `src/cells/audit-core/cell.go:167-176` |
| Status | OPEN |
| History | **New** |

**Evidence**

The interface contract says permanent failures should be dead-lettered by the subscriber implementation:

```go
// outbox.go:64-67
// Returning a non-nil error from the handler signals a transient failure
// (retry/NACK); permanent failures should be routed to a dead-letter queue
// by the implementation.
```

But the concrete `Subscriber` declares queues without any DLX arguments and uses only raw ACK/NACK behavior:

```go
// subscriber.go:109-110
ch.QueueDeclare(queueName, true, false, false, false, nil)

// subscriber.go:177-188
if err := json.Unmarshal(delivery.Body, &entry); err != nil {
    ch.Nack(delivery.DeliveryTag, false, false)
    return
}

// subscriber.go:197-203
if err := handler(ctx, entry); err != nil {
    ch.Nack(delivery.DeliveryTag, false, true)
    return
}
```

At runtime, cells receive the raw `outbox.Subscriber`; production code does not instantiate `NewConsumerBase(...)` outside tests:

```go
// bootstrap.go:266-270
if er, ok := c.(cell.EventRegistrar); ok {
    er.RegisterSubscriptions(sub)
}
```

`config-core` and `audit-core` then call `sub.Subscribe(...)` directly.

**Why it matters**

The module docs and comments promise `ConsumerBase` semantics, but the default runtime path never attaches them. In practice:

1. Permanent decode failures are broker-policy-dependent and may be dropped, because the queue declaration does not configure a dead-letter exchange.
2. Handler errors always mean `NACK + requeue`, so bad payloads can spin forever instead of being classified and dead-lettered.
3. Consumer-side idempotency is not active in the actual `bootstrap -> RegisterSubscriptions -> Subscriber` path.

This is an external semantics violation, not just an internal refactor opportunity.

**Recommendation**

Pick one safe default and make it impossible to bypass accidentally:

1. Wire `ConsumerBase` centrally in bootstrap before handing handlers to `Subscribe`.
2. Or move permanent/transient classification, retry, and DLQ publication fully into `Subscriber`.
3. If broker-native DLQ is the intended mechanism, declare queues with explicit `x-dead-letter-exchange` / `x-dead-letter-routing-key` and document it as a hard requirement.

---

## F-02: `ConsumerBase` ACKs the original message even when DLQ publish fails

| Field | Value |
|---|---|
| Seats | S3 Test/QA + S4 DevOps/SRE + S6 Product/Delivery |
| Severity | **P0** |
| Category | Data Loss / DLQ Safety |
| Files | `src/adapters/rabbitmq/consumer_base.go:134-141`, `src/adapters/rabbitmq/consumer_base.go:163-173`, `src/adapters/rabbitmq/consumer_base.go:205-212`, `src/adapters/rabbitmq/subscriber.go:211-217` |
| Status | OPEN |
| History | **Confirmed historical backlog #26** |

**Evidence**

`Wrap` returns `nil` after calling `deadLetter(...)`, which causes the caller to ACK the original delivery:

```go
// consumer_base.go:134-141
if _, ok := lastErr.(*PermanentError); ok {
    cb.deadLetter(ctx, topic, entry, lastErr, attempt+1)
    return nil
}

// consumer_base.go:163-173
cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount)
return nil
```

But `deadLetter(...)` swallows DLQ publication failure:

```go
// consumer_base.go:205-212
if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
    slog.Error("rabbitmq: failed to publish to DLQ", ...)
    return
}
```

The raw subscriber ACKs any handler that returns `nil`:

```go
// subscriber.go:211-212
if err := ch.Ack(delivery.DeliveryTag, false); err != nil { ... }
```

**Why it matters**

If the DLQ broker publish fails, the original message is acknowledged anyway. That is unrecoverable message loss: the message is neither processed successfully nor preserved in a DLQ.

**Recommendation**

Make DLQ publication part of the ACK decision:

1. Change `deadLetter(...)` to return `error`.
2. If DLQ publish fails, return a non-nil error from `Wrap(...)` so the outer subscriber does not ACK.
3. Add a unit test covering `mockPublisher.err != nil` and asserting the original delivery is not considered successfully handled.

---

## F-03: Reconnect repairs the AMQP connection object, but active subscribers still die permanently

| Field | Value |
|---|---|
| Seats | S1 Architecture + S4 DevOps/SRE + S6 Product/Delivery |
| Severity | **P0** |
| Category | Reconnect Correctness / Availability |
| Files | `src/adapters/rabbitmq/connection.go:187-225`, `src/adapters/rabbitmq/subscriber.go:154-159`, `src/runtime/bootstrap/bootstrap.go:265-273`, `src/cells/config-core/cell.go:176-183`, `src/cells/audit-core/cell.go:167-176` |
| Status | OPEN |
| History | **Confirmed historical backlog #25** |

**Evidence**

The connection layer does reconnect:

```go
// connection.go:220-224
c.reconnectWithBackoff()
close(c.connected)
```

But the subscriber exits immediately when the broker closes the delivery stream:

```go
// subscriber.go:154-159
case delivery, ok := <-deliveries:
    if !ok {
        return errcode.New(ErrAdapterAMQPConsume, "rabbitmq: delivery channel closed")
    }
```

There is no subscriber-side resubscribe loop and no `WaitConnected(...)` usage outside tests. Bootstrap just invokes `RegisterSubscriptions(sub)` once; cells log the error and stop:

```go
// config-core/cell.go:179-181
if err := sub.Subscribe(ctx, configsubscribe.TopicConfigChanged, c.subscribeSvc.HandleEvent); err != nil {
    c.logger.Error("config-subscribe: subscription ended", slog.Any("error", err))
}
```

**Why it matters**

After a transient RabbitMQ outage:

1. the underlying connection may recover,
2. but long-running consumers are gone,
3. and the process does not re-register them.

The system silently stops consuming until the whole process is restarted.

**Recommendation**

Implement reconnect at the subscription level, not only at the socket level:

1. `Subscriber.Subscribe(...)` should loop: wait for connection, declare topology, consume, and re-establish on delivery-channel closure.
2. Alternatively, bootstrap must supervise subscription goroutines and restart them on `ErrAdapterAMQPConsume`.
3. Add an integration test that kills the broker connection and verifies the same subscriber resumes consumption without process restart.

---

## F-04: `ConsumerBase` still breaks delivery semantics on idempotency failure and shutdown cancellation

| Field | Value |
|---|---|
| Seats | S1 Architecture + S3 Test/QA + S6 Product/Delivery |
| Severity | **P0** |
| Category | Idempotency / ACK Semantics |
| Files | `src/adapters/rabbitmq/consumer_base.go:106-115`, `src/adapters/rabbitmq/consumer_base.go:117-123`, `src/adapters/rabbitmq/consumer_base.go:155-159`, `src/adapters/rabbitmq/subscriber.go:197-208` |
| Status | OPEN |
| History | **Confirmed historical P0-F12D01 root cause persists, with changed failure mode** |

**Evidence**

`TryProcess(...)` claims the idempotency key before business handling:

```go
// consumer_base.go:106-107
shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
```

If Redis/idempotency is down, the code fail-opens and still processes:

```go
// consumer_base.go:108-115
if err != nil {
    // On error, default to processing
    shouldProcess = true
}
```

If shutdown/cancellation happens during retry backoff, `Wrap(...)` returns `ctx.Err()`:

```go
// consumer_base.go:155-159
case <-ctx.Done():
    return ctx.Err()
```

The outer subscriber interprets that as `NACK + requeue`:

```go
// subscriber.go:197-203
if err := handler(ctx, entry); err != nil {
    ch.Nack(delivery.DeliveryTag, false, true)
    return
}
```

**Why it matters**

Two bad outcomes remain:

1. If cancellation happens after the idempotency key was claimed but before successful completion, the redelivered message can be skipped as "already processed" even though business logic never completed.
2. If the idempotency backend errors, the message can still be processed and ACKed without a durable dedupe mark.

Both violate the stated contract "ACK after business logic + idempotency key written".

**Recommendation**

The current two-state `TryProcess(...)` model is not enough for safe shutdown semantics. The adapter needs one of:

1. a claimed/completed state machine with release-on-failure,
2. or business-layer idempotency persisted atomically with the side effect,
3. or a strict fail-closed policy when the idempotency backend is unavailable.

At minimum, do not fail-open on `TryProcess` error, and do not keep a permanent "processed" claim across `ctx.Done()` paths.

---

## F-05: `sanitizeURL` still leaks AMQP credential fragments into logs

| Field | Value |
|---|---|
| Seats | S2 Security + S4 DevOps/SRE |
| Severity | **P0** |
| Category | Credential Leakage |
| Files | `src/adapters/rabbitmq/connection.go:371-378`, `src/adapters/rabbitmq/rabbitmq_test.go:472-486` |
| Status | OPEN |
| History | **Confirmed historical P0-F12S01** |

**Evidence**

```go
// connection.go:372-377
func sanitizeURL(url string) string {
    if len(url) > 10 {
        return url[:10] + "***"
    }
    return "***"
}
```

The unit test still asserts the leaking behavior:

```go
// rabbitmq_test.go:478
{name: "long URL", url: "amqp://guest:guest@localhost:5672/", expected: "amqp://gue***"}
```

**Why it matters**

This function does not redact credentials structurally; it merely truncates the first 10 characters. For short usernames or passwords, logs can contain sensitive credential prefixes. That is enough to leak secrets into log sinks, incident exports, and support bundles.

**Recommendation**

Use `net/url.Parse`, clear `User` info completely, and log only scheme/host/vhost. The test should assert that no credential substring survives.

---

## Test / Verification Gaps

Even though `go test ./adapters/rabbitmq/...` passes, the current test suite does **not** exercise the failure modes above:

1. No test verifies that a subscriber re-establishes consumption after a broker disconnect.
2. No test verifies that DLQ publication failure prevents ACK of the original message.
3. No test covers `ctx.Done()` after `TryProcess(...)` claimed the key and before successful completion.
4. No test verifies broker-native DLQ configuration on declared queues.

---

## Final Assessment

`adapters/rabbitmq` is still not safe to treat as a production-ready delivery adapter. The biggest issue is not a single bad `if` branch; it is that the default runtime path and the documented semantics diverge. Before Round 1D can sign off, the module needs:

1. a safe default consumption path,
2. subscriber-level reconnect recovery,
3. DLQ publish success as an ACK prerequisite,
4. structurally correct credential redaction.
