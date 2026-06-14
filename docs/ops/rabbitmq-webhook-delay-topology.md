# RabbitMQ webhook delay-tier topology (ops)

Webhook dispatch honours the Svix per-attempt retry schedule (5s/5min/30min/2h/5h/10h/10h)
via broker-native delayed re-delivery (#1458, ADR `202606012052-1160-adr-webhook-retry-default.md`
§D5). Operators will see **extra RabbitMQ queues and an exchange per webhook-dispatch
subscription** — this doc explains them.

> **Broker requirement: RabbitMQ ≥ 3.10.** The delay tiers use the
> `x-dead-letter-strategy=at-least-once` queue argument, a quorum-queue feature
> introduced in 3.10 (the `quorum_queue` + `stream_queue` feature flags must be
> enabled — both are on by default from 3.12). A broker that does not meet this
> prerequisite **rejects the quorum/at-least-once declaration (or, for some
> version/arg combinations, fails to apply it)** — so a `QueueDeclare` failure on a
> *fresh* deploy points at the broker version / feature flags, not at a queue-arg
> mismatch (see the two recovery paths under "Changing the schedule" below).

## Topology

For a webhook-dispatch subscription whose dispatch queue is `Q` (derived from
`{consumerGroup}` + topic) bound to dispatch exchange `X` (the contract-ID topic):

```
X (fanout, dispatch) ──► Q (dispatch queue; x-dead-letter-exchange = real DLX)
                          └─ consumer: webhook dispatcher (signs + POSTs)

Q.delay (direct, the delay exchange)        [all Q.delay.* are quorum, at-least-once DLX]
  ├─ rk "0" ─► Q.delay.0   x-message-ttl = 5s,    no consumer, x-dead-letter-exchange = X
  ├─ rk "1" ─► Q.delay.1   x-message-ttl = 5min,  …
  ├─ rk "2" ─► Q.delay.2   x-message-ttl = 30min
  ├─ rk "3" ─► Q.delay.3   x-message-ttl = 2h
  ├─ rk "4" ─► Q.delay.4   x-message-ttl = 5h
  ├─ rk "5" ─► Q.delay.5   x-message-ttl = 10h
  └─ rk "6" ─► Q.delay.6   x-message-ttl = 10h
```

All `Q.delay*` queues are **durable quorum** queues with at-least-once
dead-lettering (#1835) — the ~40h envelope survives broker/process restart and the
internal TTL→DLX hop is publisher-confirmed on a cluster; no `x-delayed-message`
plugin is used. On the Nth POST failure the
subscriber republishes to `Q.delay.{N-1}`; on TTL expiry the message dead-letters
back to `X` → `Q` and is re-consumed. The attempt counter rides the
`x-webhook-attempt` AMQP header (broker-internal; never part of the signed HTTP
payload). Once the attempt exceeds the schedule length the entry is
`Nack(requeue=false)` → the queue's **real DLX**.

## Reliability

A delivery moves through the retry cycle in two hops, each with its own guarantee:

1. **app → delay-tier republish** — the subscriber republishes to a delay tier
   with **publisher confirms** and Acks the original delivery only after the broker
   confirms the copy, so this hop does not lose messages on a channel/broker drop.
2. **broker-internal TTL → dead-letter re-dispatch** — the delay queues are
   **quorum** queues declared with `x-dead-letter-strategy=at-least-once` (and the
   mandatory `x-overflow=reject-publish`), so the broker's internal TTL-expiry
   republish back to the dispatch exchange is **publisher-confirmed by the source
   queue**: the quorum queue retains the message until the internal confirm
   arrives. This closes the multi-node-cluster gap classic queues left open (a node
   failure mid-hop could drop one scheduled retry). #1835.

Both scheduled-retry hops are therefore **at-least-once on single-node and
clustered** deployments.

> **One terminal hop stays best-effort by design — do not over-read the guarantee.**
> Once a delivery exhausts the schedule (`attempt > len(schedule)`) it is
> `Nack(requeue=false)`'d to the **real DLX** from the **main consumer queue** `Q`,
> which is a **classic** queue — that final dead-letter hop is not
> publisher-confirmed. A message reaching it has already failed all ~40h / 7
> attempts; a node failure on this terminal routing is recoverable by manual replay
> (handlers are idempotent). Only the **scheduled delay-tier** hops are
> at-least-once; "the whole webhook retry chain is at-least-once" is **not** a
> correct reading. Quorum-ifying `Q` (shared by every non-webhook subscription) was
> deliberately out of scope for #1835.

> ⚠️ **Policy caveat.** Do **not** apply a `max-length` policy with the default
> `drop-head` overflow to the `*.delay.*` queues: at-least-once dead-lettering
> **silently degrades to at-most-once** under `drop-head` (the broker raises no
> error). The queues are declared with `reject-publish`; any operator policy that
> touches them must preserve it.

## Identifying these queues

- Name pattern: `*.delay` (exchange) and `*.delay.<tier-index>` (queues), where the
  prefix matches the dispatch queue name.
- A delay-tier queue has **no consumer** and a non-zero `x-message-ttl`; messages
  sit there until TTL expiry, so a non-empty `*.delay.*` queue is normal (deliveries
  waiting out their retry interval), **not** a stuck consumer.

## Monitoring

- Sustained growth of `*.delay.6` (the 10h tail) or messages landing in the real DLX
  indicates an endpoint that is persistently failing — inspect the DLX and the
  `webhook_deliveries_total{result="..."}` metric.
- `webhook_deliveries_total{result="circuit_open"}` counts redeliveries the
  per-endpoint circuit breaker **fast-failed without an HTTP attempt** (the target
  tripped its breaker: >5 consecutive transport/5xx/429 failures). These still flow
  through the delay tiers and reach the DLX on the same `MaxRetries` timeline — the
  breaker only suppresses the wasted POST + 30s timeout while the endpoint is open.
  A spike here pinpoints a down target without the connection churn. See ADR
  `202606140035-1541-adr-webhook-circuit-breaker.md`.
  **Runbook**: operators should configure an alert on sustained non-zero
  `webhook_deliveries_total{result="circuit_open"}` to detect endpoints that remain
  in the open state for more than one breaker timeout cycle (>60 s by default);
  sustained circuit-open indicates a persistently-down receiver that is consuming
  the retry budget without recovery — inspect the target endpoint and DLX depth.
- The delay queues are bounded per subscription (one queue per schedule tier).

## One-time classic → quorum migration (#1835)

Deployments created **before #1835** have **classic** `*.delay.*` queues. The first
deploy that carries the quorum declaration cannot change an existing queue's type in
place, so `QueueDeclare` fails with **406 PRECONDITION_FAILED** (`x-queue-type`
differs) and stops the affected webhook-dispatch subscription — the same failure mode
as a schedule change. Apply the **drain-before-recreate** procedure below once per
environment; afterwards the tiers are quorum and no further migration is needed.
Fresh deployments declare quorum tiers from the start and skip this entirely.

## Changing the schedule (PRECONDITION_FAILED / 406)

The tier `x-message-ttl` values (and `x-queue-type`) are baked into the queue
declaration. RabbitMQ does **not** allow redeclaring an existing queue with different
arguments, so changing the schedule (today fixed at `DefaultSvixSchedule`) **or
upgrading a pre-#1835 classic tier to quorum** and redeploying will fail
`QueueDeclare` with **406 PRECONDITION_FAILED** — a non-recoverable error that stops
the affected webhook-dispatch subscription. The drain-before-recreate steps below
cover both cases.

> **Two declare-failure causes — only one is a drain.** The steps below apply when an
> **existing** queue has different args (schedule change, or classic→quorum upgrade).
> If a **fresh** declare fails instead (no prior `*.delay.*` queue), the cause is the
> broker prerequisite — RabbitMQ < 3.10 or disabled `quorum_queue`/`stream_queue`
> feature flags (see "Broker requirement" at the top) — and draining does nothing; fix
> the broker, then redeploy. The runtime `declare delay tier queue` error spells out
> both recovery paths.

> ⚠️ A non-empty `*.delay.<i>` queue holds **webhooks still waiting out their retry
> interval** (see "Identifying these queues"), not garbage. Deleting it drops those
> pending retries on the floor — the webhook is never re-attempted. **Drain before
> you delete.**

To apply a new schedule **without losing in-flight retries**:

1. Stop the dispatcher (or the whole service) so no new messages enter the tiers.
2. For every old tier queue, confirm depth is **0**:
   `rabbitmqctl list_queues name messages | grep '\.delay\.'`.
   If any tier is non-empty, drain it first — shovel/republish the waiting messages
   to the dispatch exchange `X` (an immediate re-attempt) or to the real DLX for
   manual handling — and re-check until every tier reads 0.
3. Only once every tier reads 0, delete the old tier queues:
   `rabbitmqctl delete_queue Q.delay.0 … Q.delay.N` (and the `Q.delay` exchange if
   the tier count changed).
4. Redeploy — the subscriber re-declares the queues with the new TTLs.

The subscriber surfaces a 406 hint in the `declare delay tier queue` error message
that points back to this runbook.
