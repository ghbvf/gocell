# ADR 050 — MQTT Subscriber Requeue semantics (design, decision pending)

- **Status**: Proposed — decision pending (no code change yet; options enumerated for alignment)
- **Tracked**: #1286 (decision record); **decided & implemented in PR-4 #1142** (option A depends on PR-4's `$dead` DLT path)
- **Scope**: `adapters/mqtt` consume path; relationship between `outbox.Disposition` (Ack/Requeue/Reject) and MQTT v5 QoS1 manual-ack
- **Supersedes the "back-pressure caveat" framing** in ADR 048 §"QoS1 Disposition mapping" / §3 / receive-path threat review (those paragraphs are rewritten in ADR 048 §Amendment 2026-05-30 to reference this ADR)

## 1. Problem (root cause, source-grounded)

The EventBus/outbox Disposition contract was designed against AMQP (rabbitmq),
where `Requeue` → `Nack(requeue=true)` → the broker **promptly redelivers** the
message to another (or the same) consumer while other messages keep flowing.
GoCell's MQTT subscriber maps `Requeue` → **leave the QoS1 PUBLISH unacked**.
On MQTT this has two compounding properties that AMQP does not:

1. **paho flushes PUBACKs strictly in receive order.** eclipse/paho.golang's
   `acksTracker.flush` walks the received-order list and `break`s at the first
   packet whose `acknowledged` flag is false:

   ```go
   for _, v := range t.order {
       if v.acknowledged { buf = append(buf, v.pb) } else { break }
   }
   ```

   The paho manual-ack contract is explicit: *"clients send acknowledgments in
   the order in which the corresponding PUBLISH packets were received."* So a
   single un-acked (Requeue'd) message at position N **blocks the PUBACK of
   every message after it** — including ones the handler successfully Ack'd
   (`markAsAcked` sets the flag, but `flush` never reaches them).

2. **MQTT QoS1 redelivers only on session resume / reconnect.** Per MQTT v5
   spec §4.4 and mosquitto behavior, a connected client is never re-sent an
   un-acked QoS1 message; redelivery happens only when a persistent session
   reconnects. There is no per-message redelivery timer.

**Consequence**: one consumer-side transient failure (a `Requeue` that reaches
the subscriber *after* `ConsumerBase` has already exhausted its in-process retry
budget) leaves message N unacked → all later messages' PUBACKs are held → the
broker's `Receive Maximum` (mosquitto default 20) fills → **the broker stops
sending and intake stalls**, recovering only on reconnect (which then redelivers
a flood). This is a head-of-line stall, not the benign "sustained-Requeue
window exhaustion" ADR 048 originally described.

## 2. Why MQTT is structurally a poor fit here

MQTT is a pub/sub *transport*, not a work queue. It has no native NACK, no
dead-letter, and reconnect-only redelivery. Industry practice for "reliable
consumer with retry" on MQTT is one of: (a) ack-on-success + **app-level retry +
idempotency**; (b) **bridge MQTT → a real queue** (Kafka/AMQP) and do
retry/DLT there; (c) accept weak at-least-once-on-reconnect. Watermill — which
ships official NATS/Kafka/AMQP pub/subs with Nack→redelivery — has **no official
MQTT pub/sub**, precisely because nack/requeue semantics do not map cleanly.

## 3. Options

| Option | Behavior | Pros | Cons |
|--------|----------|------|------|
| **A. Ack + app-level retry** | Requeue → PUBACK the broker immediately (unblock the in-order ack stream), then re-dispatch the message to the handler in-process with bounded backoff; on retry exhaustion route to `$dead` (PR-4). | No HoL stall; prompt retry; matches "MQTT + app-level retry" industry norm. | In-process retry is **not crash-safe** — a crash mid-retry loses the message (regresses at-least-once → at-most-once-on-crash for the retry window) unless backed by a persistent retry store. Adds a retry scheduler. |
| **B. Accept reconnect-only + guardrail** | Keep leave-unacked, but bound outstanding-unacked: when unacked approaches `Receive Maximum`, force-route the oldest to `$dead` to unblock + emit a loud alert metric. | Preserves at-least-once-on-reconnect; no new retry machinery. | Still no *prompt* retry; the guardrail converts a stall into data-loss-to-`$dead` under sustained failure; complex bookkeeping. |
| **C. Bridge / out-of-scope** | Declare MQTT unsuitable as a retrying consumer; require an MQTT→AMQP/Kafka bridge for workloads needing prompt requeue; MQTT subscriber supports Ack + Reject only. | Honest about MQTT limits; simplest adapter. | Removes Requeue from the MQTT consumer contract; may not fit iotdevice (PR-5) needs. |

Cross-cutting (independent of A/B/C, already landed in PR-3): bounded concurrent
dispatch (a slow handler no longer stalls the inbound loop) — but concurrency
does **not** fix the in-order-ack HoL, which is about *un-acked* messages.

## 4. Recommendation (for discussion)

Lean **A** (ack + bounded in-process retry → `$dead` on exhaustion), accepting
the documented crash-window trade-off, because: it removes the stall, gives
prompt retry, and the producer-side transactional outbox already provides the
durable at-least-once boundary (the consumer is idempotent via the Claimer).
The crash-window regression is bounded to in-flight-retrying messages and is
strictly better than today's "stall + reconnect flood." Final decision is
deferred to the implementing PR (likely alongside PR-4's `$dead` work, since A
depends on the DLT path).

## 5. References

- eclipse/paho.golang `paho/acks_tracker.go` (`flush` in-order `break`); `paho/client.go` `Ack`/`routePublishPackets` (manual-ack ordering) — pkg.go.dev/github.com/eclipse/paho.golang/paho
- MQTT v5 spec §4.4 (Message delivery retry — reconnect-only); mosquitto `max_inflight_messages` / `receive_maximum`
- HiveMQ MQTT Essentials part 6 (QoS / Receive Maximum); mosquitto issue #897 (broker stops retransmitting without PUBACK)
- Watermill pub/sub Nack model (no official MQTT pub/sub)
- ADR 048 §Amendment 2026-05-30 (the originating review finding F2/#2)
