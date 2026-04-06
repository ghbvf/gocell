# R1D-3: adapters/rabbitmq DevOps Review

| Field | Value |
|---|---|
| Reviewer Seat | S4 DevOps / SRE |
| Scope | `src/adapters/rabbitmq/` runtime behavior, recovery, observability |
| Review basis commit | `5096d4f` |
| Date | 2026-04-06 |

---

## Summary

The adapter exposes useful operational knobs (`ReconnectBaseDelay`, `ReconnectMaxBackoff`, `ConfirmTimeout`, `PrefetchCount`, `ShutdownTimeout`), but the production recovery story is incomplete. Connection-level reconnect exists, yet active subscribers do not self-heal and the channel pool can recycle stale channels after a reconnect.

**Verdict**: `BLOCKED`

| Severity | Count | Notes |
|---|---|---|
| P0 | 1 | Reconnect is not end-to-end self-healing |
| P1 | 3 | Stale pooled channels, missing metrics, weak recovery validation |
| P2 | 1 | Setup failure can retain channels until explicit Close |

---

## Findings

### F-01 [P0] Reconnect restores the connection, but not live subscriptions

**Files**:
- `src/adapters/rabbitmq/connection.go:212-224`
- `src/adapters/rabbitmq/subscriber.go:90-132`
- `src/adapters/rabbitmq/subscriber.go:154-159`
- `src/cells/config-core/cell.go:177-182`
- `src/cells/audit-core/cell.go:170-175`

**Evidence**:

On disconnect, `Connection.reconnectLoop()` drains only the pooled channels and then reconnects:

```go
c.drainChannelPool()
...
c.reconnectWithBackoff()
```

But `Subscriber.Subscribe()` owns a dedicated consume channel acquired once at startup:

```go
ch, err := s.conn.AcquireChannel()
...
deliveries, err := ch.Consume(queueName, consumerTag, false, false, false, false, nil)
```

When the underlying AMQP channel dies, `consumeLoop()` exits permanently:

```go
case delivery, ok := <-deliveries:
    if !ok {
        return errcode.New(ErrAdapterAMQPConsume, "rabbitmq: delivery channel closed")
    }
```

Callers in `config-core` and `audit-core` simply log the returned error and stop; they do not resubscribe.

**Why it matters**:
After a transient RabbitMQ disconnect, producers may recover but consumers can remain dead until the whole process is restarted. That is a production outage, not a mere degradation.

**Recommendation**:
Move re-subscription into the adapter itself: wrap `Subscribe()` in a reconnect-aware loop that reacquires a channel and rebinds queue/exchange after delivery channel closure.

**Status**: Confirms historical issue `#25`

---

### F-02 [P1] The pool can accept stale channels from the pre-reconnect connection

**Files**:
- `src/adapters/rabbitmq/connection.go:270-281`
- `src/adapters/rabbitmq/connection.go:309-318`

**Evidence**:

`drainChannelPool()` closes only channels that are already back in the pool. Any in-flight channel held by a publisher or subscriber during disconnect is not tracked. After reconnect, `ReleaseChannel()` will accept that old channel back into the pool without validating that it still belongs to the current connection generation:

```go
func (c *Connection) ReleaseChannel(ch AMQPChannel) {
    select {
    case c.channelPool <- ch:
    default:
        _ = ch.Close()
    }
}
```

**Why it matters**:
One bad reconnect can poison the pool with dead channels, causing follow-up publish or declare failures long after the original outage.

**Recommendation**:
Track a connection generation and wrap pooled channels with their generation number. Discard channels returned from older generations instead of reusing them.

**Status**: New

---

### F-03 [P1] No metrics for publish, consume, retry, or DLQ events

**Files**:
- `src/adapters/rabbitmq/publisher.go`
- `src/adapters/rabbitmq/subscriber.go`
- `src/adapters/rabbitmq/consumer_base.go`

**Evidence**:

The module uses structured logging throughout, but exposes no counters, histograms, or hooks for:
- publish success/failure
- confirm latency
- consumer throughput
- retry count
- DLQ count
- reconnect attempts

The only DLQ observability is log output in `consumer_base.go:215-222`.

**Why it matters**:
In production, operators need to answer "Are retries spiking?", "How many messages are going to DLQ?", and "Is reconnect flapping?" without reading raw logs.

**Recommendation**:
Add a small observer interface or emit metrics through the project's observability package so the adapter can report publish/consume/retry/DLQ/reconnect counters.

**Status**: Confirms prior observability gap

---

### F-04 [P1] Recovery behavior is claimed, but not operationally validated

**Files**:
- `src/adapters/rabbitmq/integration_test.go:236-250`
- `src/adapters/rabbitmq/rabbitmq_test.go:315-438`

**Evidence**:

The code advertises auto-reconnect in `doc.go` and `connection.go`, but the recovery tests never force a real disconnect or rebind a subscriber after recovery.

**Why it matters**:
Operational features that are not validated against failure are support liabilities. This is especially risky for broker adapters, where disconnect behavior is the primary production failure mode.

**Recommendation**:
Add one recovery-focused integration test that restarts or disconnects RabbitMQ and verifies `WaitConnected()`, publish, and subscribe all recover.

**Status**: Confirmed

---

### F-05 [P2] Setup failure retains channels until explicit `Close()`

**Files**:
- `src/adapters/rabbitmq/subscriber.go:90-117`

**Evidence**:

`Subscribe()` appends the acquired channel to `s.channels` before running `Qos`, `ExchangeDeclare`, `QueueDeclare`, `QueueBind`, and `Consume`. If any of those steps fail, the function returns immediately and relies on a later `Close()` call to clean up the channel.

**Why it matters**:
This is not catastrophic, but repeated startup failures can accumulate unnecessary open channels if callers abandon the subscriber after the first error.

**Recommendation**:
Close and remove the just-acquired channel on setup failure before returning.

**Status**: New

