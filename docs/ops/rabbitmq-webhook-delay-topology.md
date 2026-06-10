# RabbitMQ webhook delay-tier topology (ops)

Webhook dispatch honours the Svix per-attempt retry schedule (5s/5min/30min/2h/5h/10h/10h)
via broker-native delayed re-delivery (#1458, ADR `202606012052-1160-adr-webhook-retry-default.md`
§D5). Operators will see **extra RabbitMQ queues and an exchange per webhook-dispatch
subscription** — this doc explains them.

## Topology

For a webhook-dispatch subscription whose dispatch queue is `Q` (derived from
`{consumerGroup}` + topic) bound to dispatch exchange `X` (the contract-ID topic):

```
X (fanout, dispatch) ──► Q (dispatch queue; x-dead-letter-exchange = real DLX)
                          └─ consumer: webhook dispatcher (signs + POSTs)

Q.delay (direct, the delay exchange)
  ├─ rk "0" ─► Q.delay.0   x-message-ttl = 5s,    no consumer, x-dead-letter-exchange = X
  ├─ rk "1" ─► Q.delay.1   x-message-ttl = 5min,  …
  ├─ rk "2" ─► Q.delay.2   x-message-ttl = 30min
  ├─ rk "3" ─► Q.delay.3   x-message-ttl = 2h
  ├─ rk "4" ─► Q.delay.4   x-message-ttl = 5h
  ├─ rk "5" ─► Q.delay.5   x-message-ttl = 10h
  └─ rk "6" ─► Q.delay.6   x-message-ttl = 10h
```

All `Q.delay*` queues are **durable** (the ~40h envelope survives broker/process
restart — no `x-delayed-message` plugin is used). On the Nth POST failure the
subscriber republishes to `Q.delay.{N-1}`; on TTL expiry the message dead-letters
back to `X` → `Q` and is re-consumed. The attempt counter rides the
`x-webhook-attempt` AMQP header (broker-internal; never part of the signed HTTP
payload). Once the attempt exceeds the schedule length the entry is
`Nack(requeue=false)` → the queue's **real DLX**.

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
- The delay queues are bounded per subscription (one queue per schedule tier).

## Changing the schedule (PRECONDITION_FAILED / 406)

The tier `x-message-ttl` values are baked into the queue declaration. RabbitMQ does
**not** allow redeclaring an existing queue with different arguments, so changing the
schedule (today fixed at `DefaultSvixSchedule`) and redeploying will fail
`QueueDeclare` with **406 PRECONDITION_FAILED** — a non-recoverable error that stops
the affected webhook-dispatch subscription. To apply a new schedule:

1. Stop the dispatcher (or the whole service).
2. Delete the old tier queues: `rabbitmqctl delete_queue Q.delay.0 … Q.delay.N`
   (and `Q.delay` exchange if the tier count changed).
3. Redeploy — the subscriber re-declares the queues with the new TTLs.

The subscriber surfaces a hint in the `declare delay tier queue` error message when
it hits 406.
