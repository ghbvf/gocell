# R1D-3: adapters/rabbitmq Six-Role Review Summary

| Field | Value |
|---|---|
| Module | `src/adapters/rabbitmq/` |
| Review round | `R1D-3` |
| Review basis commit | `5096d4f` |
| Date | 2026-04-06 |
| Signoff | `BLOCKED` |
| Verification note | `/opt/homebrew/bin/go test ./adapters/rabbitmq/...` passed; coverage `78.7%`; integration-tag suite not run |

---

## Scope

This adjudication consolidates the six seats for `R1D-3`:

| Seat | Artifact |
|---|---|
| S1 Architecture | `docs/reviews/202604060830-R0/R1D-3-rabbitmq-architect.md` |
| S2 Security | `docs/reviews/202604060830-R0/R1D-3-rabbitmq-security.md` |
| S3 Testing | `docs/reviews/202604060830-R0/R1D-3-rabbitmq-testing.md` |
| S4 DevOps | `docs/reviews/202604060830-R0/R1D-3-rabbitmq-devops.md` |
| S5 DX / Maintainability | `docs/reviews/202604060830-R0/R1D-3-rabbitmq-codestyle.md` |
| S6 Product | `docs/reviews/202604060830-R0/R1D-3-rabbitmq-product.md` |

When an older seat draft disagrees with the current code at `5096d4f`, the adjudicated findings below are the canonical result for this round.

---

## Executive Summary

`adapters/rabbitmq` has a solid outer shape: dependency direction is clean, the adapter surface is small, and the tests cover many happy/error paths. But four delivery-safety issues still block signoff:

1. AMQP credentials can still leak into logs.
2. DLQ publish failure still ACKs the original message.
3. `TryProcess` now claims idempotency before business success, which can silently drop requeued messages.
4. Reconnect restores the AMQP connection but does not revive live subscribers, and stale channels can be recycled after reconnect.

**Adjudicated count**: `4 P0`, `5 P1`, `3 P2`

---

## Adjudicated Findings

### P0-1: `sanitizeURL` still leaks AMQP credential fragments

**Files**:
- `src/adapters/rabbitmq/connection.go:182-183`
- `src/adapters/rabbitmq/connection.go:371-378`
- `src/adapters/rabbitmq/rabbitmq_test.go:472-486`

**Evidence**:

```go
slog.Info("rabbitmq: connection established",
    slog.String("url", sanitizeURL(c.config.URL)))
```

```go
func sanitizeURL(url string) string {
    if len(url) > 10 {
        return url[:10] + "***"
    }
    return "***"
}
```

The test suite explicitly codifies the leak:

```go
{name: "long URL", url: "amqp://guest:guest@localhost:5672/", expected: "amqp://gue***"},
```

**Why it matters**:
This is direct secret leakage to logs. It remains a merge blocker.

**Disposition**: Confirms prior finding `F-12S-01`

---

### P0-2: DLQ publish failure is swallowed, so the original delivery is ACKed

**Files**:
- `src/adapters/rabbitmq/consumer_base.go:140-141`
- `src/adapters/rabbitmq/consumer_base.go:170-173`
- `src/adapters/rabbitmq/consumer_base.go:205-212`
- `src/adapters/rabbitmq/subscriber.go:197-212`

**Evidence**:

`Wrap()` unconditionally returns `nil` after routing to DLQ:

```go
cb.deadLetter(ctx, topic, entry, lastErr, cb.config.RetryCount)
return nil
```

But `deadLetter()` only logs publish failure:

```go
if err := cb.publisher.Publish(ctx, dlqTopic, payload); err != nil {
    slog.Error("rabbitmq: failed to publish to DLQ", ...)
    return
}
```

The subscriber ACKs on `nil` handler return.

**Why it matters**:
If DLQ publish fails, the message is neither processed nor parked in DLQ, but it is still ACKed. That is silent data loss.

**Disposition**: Confirms historical plan item `#26`

---

### P0-3: `TryProcess` preclaims the idempotency key before business success

**Files**:
- `src/adapters/rabbitmq/consumer_base.go:98`
- `src/adapters/rabbitmq/consumer_base.go:106-123`
- `src/adapters/rabbitmq/consumer_base.go:155-159`
- `src/adapters/rabbitmq/subscriber.go:197-203`
- `docs/reviews/202604060830-R0/PR35-noncompat-findings.md`

**Evidence**:

The contract comment says:

```go
// return nil -> ACK (business logic succeeded, idempotency key written)
```

But the implementation claims the key first:

```go
shouldProcess, err := cb.checker.TryProcess(ctx, idempotencyKey, cb.config.IdempotencyTTL)
```

If the handler later returns an error to the subscriber, the message is NACKed and requeued. On redelivery, `TryProcess` returns `false` and the subscriber skips the message as "already processed".

**Why it matters**:
This degrades at-least-once delivery into silent message loss on shutdown, crash, panic, or any non-terminal failure after `TryProcess` succeeds.

**Disposition**: Current-code regression; treat as canonical blocker for `5096d4f`

---

### P0-4: Reconnect is not end-to-end self-healing for consumers

**Files**:
- `src/adapters/rabbitmq/connection.go:212-224`
- `src/adapters/rabbitmq/connection.go:309-318`
- `src/adapters/rabbitmq/subscriber.go:90-132`
- `src/adapters/rabbitmq/subscriber.go:154-159`
- `src/cells/config-core/cell.go:177-182`
- `src/cells/audit-core/cell.go:170-175`

**Evidence**:

The connection reconnect loop restores only the connection and pooled channels. Subscribers own dedicated consume channels and exit permanently when `deliveries` closes:

```go
if !ok {
    return errcode.New(ErrAdapterAMQPConsume, "rabbitmq: delivery channel closed")
}
```

Cells currently just log that error and stop.

Additionally, old in-flight channels can be returned to the pool after reconnect because `ReleaseChannel()` has no generation check.

**Why it matters**:
After a transient broker outage, publishers may recover while consumers remain dead or keep reusing stale channels. This is a production outage path.

**Disposition**: Confirms historical issue `#25`

---

### P1-1: Invalid JSON is dropped, not dead-lettered, despite the documented contract

**Files**:
- `src/adapters/rabbitmq/consumer_base.go:101`
- `src/adapters/rabbitmq/subscriber.go:177-188`
- `src/adapters/rabbitmq/subscriber.go:109-117`

**Why it matters**:
The comment says unmarshal failures should dead-letter, but the implementation simply `Nack(..., requeue=false)` without any DLX setup or explicit DLQ publish. Malformed external input is therefore discarded, not quarantined.

---

### P1-2: Wrapped `PermanentError` values are misclassified as transient

**Files**:
- `src/adapters/rabbitmq/consumer_base.go:133-141`

**Why it matters**:
The code uses `lastErr.(*PermanentError)` instead of `errors.As`, so wrapped permanent errors retry unnecessarily and bypass the intended fast-DLQ path.

---

### P1-3: No explicit operational metrics for publish / consume / retry / DLQ / reconnect

**Files**:
- `src/adapters/rabbitmq/publisher.go`
- `src/adapters/rabbitmq/subscriber.go`
- `src/adapters/rabbitmq/consumer_base.go`
- `src/adapters/rabbitmq/connection.go`

**Why it matters**:
Logs exist, but operators still cannot answer basic production questions without reading raw log streams.

---

### P1-4: `PrefetchCount` does not translate into concurrent processing

**Files**:
- `src/adapters/rabbitmq/subscriber.go:161-162`
- `src/adapters/rabbitmq/subscriber.go:167-218`

**Why it matters**:
`processDelivery()` runs synchronously in the consume loop, so prefetch mainly buffers messages instead of increasing throughput.

---

### P1-5: Recovery and DLQ-failure paths are still under-tested

**Files**:
- `src/adapters/rabbitmq/rabbitmq_test.go`
- `src/adapters/rabbitmq/integration_test.go`

**Why it matters**:
The two most important failure modes for this adapter are reconnect and DLQ routing. Neither is proven end to end in the current suite.

---

### P2-1: Published AMQP messages have no `MessageId`

**Files**:
- `src/adapters/rabbitmq/publisher.go:50-55`

**Why it matters**:
Broker-level traceability and downstream debugging are weaker than they need to be.

---

### P2-2: Successful DLQ routing is logged at `Error` level

**Files**:
- `src/adapters/rabbitmq/consumer_base.go:215-222`

**Why it matters**:
This inflates error budgets and makes dashboards noisier than the actual system state.

---

### P2-3: RabbitMQ documentation and examples still lag the actual adapter surface

**Files**:
- `docs/guides/adapter-config-reference.md`
- `examples/`

**Why it matters**:
The code is ahead of the onboarding material, which raises integration cost even where the implementation is sound.

---

## Recommended Next Actions

1. Fix the four P0 items before any R1D adapter signoff.
2. After fixes, rerun `R1D-3` with emphasis on reconnect, DLQ failure, and idempotency semantics.
3. After the fixes, rerun `/opt/homebrew/bin/go test ./adapters/rabbitmq/...` and then the relevant `-tags integration` suite to validate the repaired paths.
