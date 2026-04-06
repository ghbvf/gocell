# R1D-3: adapters/rabbitmq Independent Review

| Field | Value |
|---|---|
| Mode | Fresh independent review |
| Scope | `src/adapters/rabbitmq/` plus direct runtime wiring |
| Method | Source + tests + local verification only; no existing review reports consulted |
| Review basis commit | `5096d4f` |
| Date | 2026-04-06 |
| Signoff | `BLOCKED` |

---

## Verification

Executed from [`src`](/Users/shengming/Documents/code/gocell/src):

- `/opt/homebrew/bin/go test ./adapters/rabbitmq/...` -> `FAIL`
- `/opt/homebrew/bin/go test -cover ./adapters/rabbitmq/...` -> `FAIL` (`79.4%` statements, but run fails)
- `/opt/homebrew/bin/go test -run 'TestConsumerBase_Wrap_DLQPublishFails|TestSubscriber_ProcessDelivery_CtxCancelled|TestSanitizeURL|TestConsumerBase_Wrap_WrappedPermanentError' ./adapters/rabbitmq/...` -> only `TestSanitizeURL` fails

This matters because several historically risky paths are now fixed in code, but the package still is not in a releasable state.

---

## Findings

### 1. [P0] Default runtime wiring bypasses `ConsumerBase`, but consumer handlers are written as if it exists

**Files**
- [`bootstrap.go:265`](/Users/shengming/Documents/code/gocell/src/runtime/bootstrap/bootstrap.go#L265)
- [`bootstrap.go:270`](/Users/shengming/Documents/code/gocell/src/runtime/bootstrap/bootstrap.go#L270)
- [`cell.go:176`](/Users/shengming/Documents/code/gocell/src/cells/config-core/cell.go#L176)
- [`cell.go:179`](/Users/shengming/Documents/code/gocell/src/cells/config-core/cell.go#L179)
- [`cell.go:167`](/Users/shengming/Documents/code/gocell/src/cells/audit-core/cell.go#L167)
- [`cell.go:172`](/Users/shengming/Documents/code/gocell/src/cells/audit-core/cell.go#L172)
- [`service.go:63`](/Users/shengming/Documents/code/gocell/src/cells/config-core/slices/configsubscribe/service.go#L63)
- [`service.go:74`](/Users/shengming/Documents/code/gocell/src/cells/config-core/slices/configsubscribe/service.go#L74)

**Evidence**

Bootstrap passes the raw `outbox.Subscriber` directly into cells:

```go
if er, ok := c.(cell.EventRegistrar); ok {
    er.RegisterSubscriptions(sub)
}
```

Cells then subscribe with raw handlers:

```go
if err := sub.Subscribe(ctx, configsubscribe.TopicConfigChanged, c.subscribeSvc.HandleEvent); err != nil { ... }
```

But repo-wide construction of `NewConsumerBase(...)` appears only in rabbitmq tests and integration tests, not in production wiring.

At the same time, real handlers are authored as if `ConsumerBase` is present. For example `configsubscribe` says:

```go
// Permanent error: return error so ConsumerBase routes to dead letter
return fmt.Errorf("config-subscribe: unmarshal payload: %w", err)
```

**Why it matters**

The default RabbitMQ path in this repo does not actually provide the idempotency / retry classification / DLQ behavior that handlers and package comments assume. In production that means:

- consumer-side idempotency is absent by default
- poison messages can requeue forever instead of being classified
- delivery semantics depend on each cell hand-rolling behavior it currently does not hand-roll

This is a real runtime semantics bug, not just missing convenience wiring.

**Recommendation**

Either wire `ConsumerBase` centrally in bootstrap before handlers are passed to `Subscribe`, or move the promised retry/DLQ semantics into `Subscriber` itself so the default runtime path is safe.

---

### 2. [P0] Connection reconnect does not restore live subscribers

**Files**
- [`connection.go:188`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L188)
- [`connection.go:221`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L221)
- [`subscriber.go:121`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L121)
- [`subscriber.go:154`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L154)
- [`subscriber.go:158`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L158)
- [`cell.go:179`](/Users/shengming/Documents/code/gocell/src/cells/config-core/cell.go#L179)
- [`cell.go:172`](/Users/shengming/Documents/code/gocell/src/cells/audit-core/cell.go#L172)

**Evidence**

`Connection` has a reconnect loop:

```go
c.reconnectWithBackoff()
close(c.connected)
```

But each subscriber holds a one-time `Consume(...)` stream. If that stream closes, the subscriber exits:

```go
if !ok {
    return errcode.New(ErrAdapterAMQPConsume, "rabbitmq: delivery channel closed")
}
```

There is no resubscribe loop, and `WaitConnected()` is not used anywhere outside tests. Current cell callers only log the returned error and stop.

**Why it matters**

After a transient broker outage, the socket can recover while consumers stay dead forever. That is a production availability failure on the primary consume path.

**Recommendation**

Make `Subscriber.Subscribe` reconnect-aware: reacquire channel, redeclare topology, and resume consuming after delivery-channel closure. If that is intentionally outside the adapter, bootstrap must supervise and restart subscriptions.

---

### 3. [P0] Shutdown/cancel path can discard in-flight messages by default

**Files**
- [`subscriber.go:109`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L109)
- [`subscriber.go:110`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L110)
- [`subscriber.go:197`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L197)
- [`subscriber.go:201`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L201)
- [`subscriber.go:206`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L206)

**Evidence**

Queues are declared with no DLX/DLQ arguments:

```go
ch.QueueDeclare(queueName, true, false, false, false, nil)
```

When a handler returns an error after context cancellation, the adapter does:

```go
if ctx.Err() != nil {
    ch.Nack(delivery.DeliveryTag, false, false)
    return
}
```

The inline comment claims the message will be picked up by another consumer or redelivered after reconnect, but `Nack(..., requeue=false)` does not do that unless broker-side dead-lettering is configured elsewhere.

**Why it matters**

On the default path implemented by this adapter, shutdown can turn an in-flight transient error into a dropped message. This is direct delivery-semantics breakage.

**Recommendation**

Either:

- configure dead-lettering explicitly in the declared queue topology, or
- keep requeue semantics and solve the storm another way, or
- distinguish "graceful drain" from "broker discard" with a documented broker policy requirement

But the current default is unsafe.

---

### 4. [P0] Shared `Subscriber` default queue naming collapses fanout delivery into competing consumers

**Files**
- [`subscriber.go:23`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L23)
- [`subscriber.go:85`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L85)
- [`subscriber.go:87`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/subscriber.go#L87)
- [`bootstrap.go:266`](/Users/shengming/Documents/code/gocell/src/runtime/bootstrap/bootstrap.go#L266)
- [`cell.go:179`](/Users/shengming/Documents/code/gocell/src/cells/config-core/cell.go#L179)
- [`cell.go:172`](/Users/shengming/Documents/code/gocell/src/cells/audit-core/cell.go#L172)

**Evidence**

The runtime injects one `outbox.Subscriber` into all cells:

```go
er.RegisterSubscriptions(sub)
```

`Subscriber` derives queue name from a single shared config:

```go
queueName := s.config.QueueName
if queueName == "" {
    queueName = topic
}
```

Both `config-core` and `audit-core` subscribe to `event.config.changed.v1`, but they do so through the same shared subscriber object. With the default naming, both become consumers on the same queue for the same topic.

**Why it matters**

For a fanout-style event contract, these cells should each receive a copy. Instead, the current default turns them into competing consumers, so one cell can consume an event the other never sees.

That is a direct semantic break in cross-cell event delivery.

**Recommendation**

Queue identity must include subscriber identity, not just topic. At minimum, default queue naming should incorporate cell or consumer-group identity, or `QueueName` should be required per logical subscriber rather than shared globally.

---

### 5. [P1] The package test suite is currently red because `sanitizeURL()` and its tests disagree

**Files**
- [`connection.go:372`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L372)
- [`connection.go:379`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L379)
- [`rabbitmq_test.go:473`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/rabbitmq_test.go#L473)
- [`rabbitmq_test.go:514`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/rabbitmq_test.go#L514)

**Evidence**

Implementation:

```go
u.User = url.UserPassword("***", "***")
return u.String()
```

This produces URL-escaped credentials such as:

```text
amqp://%2A%2A%2A:%2A%2A%2A@localhost:5672/
```

But the tests expect unescaped literal `***`:

```go
expected: "amqp://***:***@localhost:5672/"
```

That mismatch currently fails `go test ./adapters/rabbitmq/...`.

**Why it matters**

This is not a security leak anymore, but it does leave the module with a red local test suite. That blocks trustworthy verification of the rest of the package.

**Recommendation**

Pick one canonical representation and align both sides. If the log-safe output should stay URL-valid, update the tests to expect the escaped form. If human-readable output is preferred, build the redacted string manually instead of relying on `url.UserPassword(...).String()`.

---

### 6. [P1] Reconnect can poison the channel pool with stale channels

**Files**
- [`connection.go:271`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L271)
- [`connection.go:274`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L274)
- [`connection.go:311`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L311)
- [`connection.go:313`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/connection.go#L313)

**Evidence**

On disconnect, `drainChannelPool()` closes only channels already sitting in the pool. Any channel checked out during the outage is not tracked. Later, `ReleaseChannel()` blindly re-adds whatever it is given:

```go
case c.channelPool <- ch:
```

There is no generation check tying a channel to the current underlying AMQP connection.

**Why it matters**

After reconnect, old dead channels can re-enter the pool and fail later publishes or subscribes long after the original outage.

**Recommendation**

Track connection generation and discard channels created from older generations, or validate channel health before pooling.

---

### 7. [P2] Recovery and full consume-chain integration tests are still weaker than the code claims

**Files**
- [`integration_test.go:59`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go#L59)
- [`integration_test.go:82`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go#L82)
- [`integration_test.go:99`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go#L99)
- [`integration_test.go:147`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go#L147)
- [`integration_test.go:214`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go#L214)
- [`integration_test.go:236`](/Users/shengming/Documents/code/gocell/src/adapters/rabbitmq/integration_test.go#L236)

**Evidence**

- `TestIntegration_PublishConsume` publishes before subscriber setup, so it does not reliably prove a real publish-then-consume round-trip.
- `TestIntegration_ConsumerBaseRetry` directly calls `wrappedHandler` instead of consuming through `Subscriber`.
- `TestIntegration_ConnectionRecovery` never actually forces a disconnect.

**Why it matters**

The most important behavior here is recovery and end-to-end delivery semantics. The current integration suite gives less confidence than its names suggest.

**Recommendation**

Strengthen the integration tests so they actually exercise:

- subscriber-ready before publish
- `Subscriber.Subscribe(..., cb.Wrap(...))` as a full chain
- real disconnect/reconnect behavior

---

## Current-State Notes

These items appear fixed in `5096d4f` and should not be carried forward as open bugs in a fresh review:

- `deadLetter()` now returns errors instead of silently ACKing on DLQ publish failure.
- wrapped `PermanentError` values are now detected via `errors.As(...)`.
- credential fragments are no longer leaked by prefix truncation; the current issue is test mismatch, not secret exposure.
