# ADR 050 — MQTT Subscriber Requeue semantics

- **Status**: **Accepted** (Option C, decided & implemented in PR-4 #1142)
- **Tracked**: #1286 (decision record, closed by PR-4 #1142)
- **Scope**: `adapters/mqtt` consume path; relationship between `outbox.Disposition` (Ack/Requeue/Reject) and MQTT v5 QoS1 manual-ack
- **Supersedes the "back-pressure caveat" framing** in ADR 048 §"QoS1 Disposition mapping" / §3 / receive-path threat review (those paragraphs are rewritten in ADR 048 §Amendment 2026-05-30 and updated again below in §Amendment 2026-05-30 to reflect Option C)

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

## 4. Decision

The decision is **Option C** — see §6 for the accepted semantics and rationale
(open-source consensus + ConsumerBase-is-sole-retry-layer + simplicity). §3
records the three options weighed; §6 is the single authoritative narrative.

(An earlier draft of this ADR leaned toward Option A; that pre-decision
recommendation was reversed during implementation and is intentionally **not**
retained here, per `.claude/rules/gocell/ai-robust.md` §"ADR amendment 落地必查"
— no contradictory prose alongside the final decision.)

## 5. References

- eclipse/paho.golang `paho/acks_tracker.go` (`flush` in-order `break`); `paho/client.go` `Ack`/`routePublishPackets` (manual-ack ordering) — pkg.go.dev/github.com/eclipse/paho.golang/paho
- MQTT v5 spec §4.4 (Message delivery retry — reconnect-only); mosquitto `max_inflight_messages` / `receive_maximum`
- HiveMQ MQTT Essentials part 6 (QoS / Receive Maximum); mosquitto issue #897 (broker stops retransmitting without PUBACK)
- Watermill pub/sub Nack model (no official MQTT pub/sub); Spring Integration MQTT (ack-only); HiveMQ Client (ack-only)
- ADR 048 §Amendment 2026-05-30 (the originating review finding F2/#2)

---

## 6. Decision (2026-05-30, PR-4 #1142)

**决策：Option C。** §4 的 Lean A 倾向被推翻；最终选定的适配器语义为：

- `DispositionAck` → PUBACK（成功路径）
- `DispositionReject` → 发布到 `$dead/<originalTopic>`，然后 PUBACK-as-poison（停止 broker 重投）；指标 `mqtt_dlx_total` 计数
- `DispositionRequeue` → **leave-unacked**（不发 PUBACK）；幂等 Claim Release；适配器声明 `outboxtest.Features.SupportsRequeue = false`

适配器不引入任何 in-process 重试调度器，不引入 `Receive Maximum` 护栏。

### 为何选 C 而不是 A

#### 1. OSS 共识：MQTT 是 pub/sub 传输层，不是工作队列

MQTT 没有原生 NACK，重投只在 session 恢复 / 重连时发生，工业界早有定论：

- **eclipse/paho.golang**：只暴露 `Ack`，没有 Nack——"clients send acknowledgments in the order in which the corresponding PUBLISH packets were received"。
- **Spring Integration MQTT**：ack-only，无 Nack 接口。
- **HiveMQ Client**（Java）：ack-only。
- **Watermill**：没有官方 MQTT pub/sub 实现，原因正是 Nack/Requeue 语义无法诚实地映射到 MQTT。

工业界对"MQTT + 可靠消费"的成熟模式是：ack-on-success + **app-level retry + idempotency**，或 **MQTT → Kafka/AMQP 桥接**后在真正的队列上做 retry/DLT。Option C 与这一共识对齐。

#### 2. ConsumerBase 已是唯一重试层

`kernel/outbox/consumer_base.go` 的 `retryLoop`（行 582–651）对 handler 返回的 `Requeue(err)` 在进程内按退避策略重试 `RetryCount` 次，耗尽后转换为 **`DispositionReject`（ProcessReason="retry_exhausted"）** 路由到 DLX——**不会把瞬态 Requeue 透传到 adapter 层**。

> **注意**：`consumer_base.go` 行 343–349 的 doc comment 写的是"handler 返回 DispositionRequeue → 直接透传"，但该注释已过时，与实际的 `retryLoop` 实现相矛盾。以代码实现为准，不以注释为准。

适配器收到 `DispositionRequeue` **仅在以下三种降级路径**：
- **优雅关闭**（ctx cancel，retryLoop 行 617/626）：adapter 此时 leave-unacked → 重连后 broker 重投，语义完全正确。
- **幂等后端不可用，fail-closed**（Claimer 故障，行 390）：同上，leave-unacked 等待恢复，prompt 重投没有意义（后端本身宕了）。
- **Claim 争抢（ClaimBusy）**：同上。

在所有这些降级状态下，MQTT 原生的 leave-unacked-then-redeliver-on-reconnect 是正确行为；prompt in-process 重试反而毫无意义（我们正在关机 / 后端宕了 / fail-closed 消费本就停滞）。

#### 3. 当前没有任何生产路径需要 prompt MQTT Requeue

- **PR-5 iotdevice**（spec.md 明确）：publish-only，无消费端。
- **唯一返回 Requeue 的生产 handler**（`cells/accesscore/slices/configreceive/service.go`）：RabbitMQ-only。
- **`outboxtest` conformance 的 Requeue 子测试**（`testDispositionRequeue`、`testZeroValueDisposition`、`testReceiptReleasedOnRequeue`）：全部在 `SupportsRequeue = false` 时 SKIP。

#### 4. 最简洁、最诚实

Option C 相比 A 代码更少（无重试调度器），对 MQTT 的能力边界保持诚实。当前 `subscriber.go` 的 Requeue 分支（leave-unacked + Release + observe）**已经是 Option C 行为**，无需改动语义，只需补声明 `SupportsRequeue = false` 并在本 ADR 记录决策。

### HoL stall 不影响稳态

§1 / §2 描述的 head-of-line stall 在 Option C 下结构上无法在稳态积累：ConsumerBase 把瞬态失败在进程内重试并在耗尽后转 Reject（→ `$dead`），adapter 在正常业务路径上永远不会收到 `DispositionRequeue`；只有上述三种降级状态才会触发，而这些状态本身就是系统非稳态（关机/后端宕/争抢），leave-unacked 是预期行为。

### $dead DLT 已在本 PR 实施

`DispositionReject`（含 ConsumerBase retry_exhausted 转换的 Reject）和 unmarshal poison 均在 PUBACK-as-poison 之前 publish 到 `$dead/<originalTopic>`，通过 sealed `TopicNamespace.MintDeadLetter` funnel 路由，指标 `mqtt_dlx_total` 可观测。
