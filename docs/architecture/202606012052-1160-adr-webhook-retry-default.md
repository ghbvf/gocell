# ADR: Webhook retry defaults — Svix schedule, HTTP status mapping, delivery timeout, header names

- Status: Accepted
- Date: 2026-06-01
- Context: KERNEL-WEBHOOK-01 PR-5 (#1160)
- Supersedes / amends: none (greenfield)

## Context

The outbound webhook dispatcher (`kernel/webhook.Dispatcher`, PR-5) delivers
signed HTTP POST requests to external targets and must define:

1. How many times to retry a failed delivery, and with what inter-attempt delays.
2. Which HTTP status codes and transport errors are permanent failures (→ DLX)
   versus transient (→ retry).
3. How long each single attempt may run before being aborted.
4. Which header names carry the three signature fields on the wire.

These choices are made once at the kernel level and drive the runtime dispatch
consumer in `runtime/webhook/dispatch`. Recording them in an ADR makes the
trade-offs explicit, citable, and auditable.

## Decision

### D1 — Svix retry schedule as the canonical default

`DefaultSvixSchedule()` returns a `RetrySchedule` with **7 retries** after the
immediate first attempt — **8 deliveries total** — at the following inter-attempt
delays:

| Retry # | Delay before this attempt |
|---------|--------------------------|
| 1       | 5 s                      |
| 2       | 5 min                    |
| 3       | 30 min                   |
| 4       | 2 h                      |
| 5       | 5 h                      |
| 6       | 10 h                     |
| 7       | 10 h                     |

Source of truth: the Svix official retry table at
`https://docs.svix.com/retries`. The standard-webhooks specification
(`standard-webhooks/standard-webhooks spec/standard-webhooks.md`) describes a
10-step variant; we adopt the Svix 8-delivery default as the more widely
deployed real-world baseline. Operators can override the schedule by providing
a custom `RetrySchedule` via a future `WithSchedule` option (not in PR-5 scope).

`RetrySchedule.MaxRetries()` returns 7 (the retry count excluding the initial
attempt); `RetrySchedule.Attempts()` returns 8 (the total delivery count).

**PR-5 ships `DefaultSvixSchedule` as a tested primitive and the documented
seam for the broker-delay follow-up. PR-5 does NOT wire per-attempt delays or
a per-dispatcher RetryCount at runtime**: `Dispatcher` has no schedule field,
`runtime/webhook/dispatch.BuildConsumers` never sets `ConsumerBaseConfig.RetryCount`,
and the global `kernel/outbox.ConsumerBase` cannot accept a per-dispatcher
count. The dispatch consumer rides the shared ConsumerBase (default exponential
backoff, capped 30 s). `RetrySchedule.DelayFor` is the seam the follow-up
(gh #1458) will consume.

### D2 — HTTP status code → `outbox.HandleResult` mapping (standard-webhooks aligned)

> **Amendment 2026-06-02 (review #1455, finding F3).** D2 originally rejected 3xx
> and all 4xx (except 408/429) as **permanent**. That contradicted this ADR's own
> "standard-webhooks / Svix aligned" claim: Svix, Standard Webhooks, and Convoy
> all treat **every non-2xx as a delivery failure the sender retries** — the
> sender cannot tell a permanent 4xx (e.g. a receiver bug) from a transient one
> (secret rotation → 401, deploy gap → 404, throttle). The mapping below is the
> amended, aligned truth; the pre-amendment "Reject 4xx/3xx" rows are removed (no
> dual truth source, per `.claude/rules/gocell/ai-robust.md` "ADR amendment 落地必查").

The single classification function is `kernel/webhook/retry.go::Classify(statusCode
int, transportErr error) outbox.Disposition`. The rule is now simply: **2xx → Ack;
every other status → Requeue; an SSRF-blocked transport error → Reject**.

| Outcome | Disposition | Rationale |
|---------|-------------|-----------|
| 2xx | `DispositionAck` (Ack) | Delivery succeeded — the only acknowledgement. |
| Every non-2xx status (3xx, 4xx, 5xx) | `DispositionRequeue` (transient) | standard-webhooks / Svix: the receiver MUST return 2xx to acknowledge; any other status is a delivery failure. A receiver's 4xx/3xx is usually transient on its side (deploy gap → 404, secret rotation → 401, throttle → 429, server fault → 5xx), and the sender cannot distinguish permanent from transient — so it retries until the budget is exhausted, at which point `ConsumerBase` escalates to Reject → DLX. |
| SSRF-blocked (`ErrWebhookSSRFBlocked`) | `DispositionReject` (permanent → DLX) | A misconfigured or hostile target URL (bad scheme, embedded userinfo, blocked dial, denied redirect). The target will not become safe on retry; operator intervention is required to correct the webhook endpoint registration. This is the **only** permanent classification outcome. |
| DNS resolution failure / connection refused / timeout / network error (non-SSRF) | `DispositionRequeue` (transient) | Transient connectivity fault; retry after back-off. A DNS failure is fail-closed at the dial layer (no connection is made) but is **not** an SSRF block — conflating "could not resolve" with "resolved to a blocked address" would permanently dead-letter deliveries on a DNS hiccup (review #1455 F1). |

**Note on 3xx and redirects**: a 3xx status almost never reaches the `Classify`
status branch, because `SafePolicy.DenyRedirect` (wired as the `*http.Client`
`CheckRedirect`) returns an error on the first redirect, which surfaces as a
transport error carrying `ErrWebhookSSRFBlocked` → `DispositionReject`. So a
redirect attempt is **permanent** (an SSRF safety stance: one-shot delivery
never follows a redirect to a potentially-unvetted Location). A bare 3xx that
somehow reached the status branch is treated like any other non-2xx → Requeue.

### D3 — Delivery timeout default 30 s

`defaultDeliveryTimeout = 30 * time.Second` bounds each individual HTTP
delivery attempt. This is the `*http.Client.Timeout` value set in
`NewDispatcher`.

Three reference points bracket the choice:

- **Svix**: 15 s (`https://docs.svix.com/retries`).
- **standard-webhooks spec**: recommends 15–30 s for sender-side timeouts
  (`standard-webhooks/standard-webhooks spec/standard-webhooks.md`).
- **Convoy**: 30 s (`https://www.getconvoy.io/docs`).

We default to 30 s — the upper end of the standard-webhooks recommendation and
Convoy's default — because some downstream receivers perform synchronous work
(database writes, synchronous API calls) before acknowledging. A generous
default reduces spurious timeouts that would unnecessarily consume retry budget.
Operators who need tighter control can call `WithDeliveryTimeout(d)`.

### D4 — Vendor-neutral outbound signature header names

Outbound HTTP deliveries carry three signature headers:

| Header name | Content |
|-------------|---------|
| `webhook-id` | Per-delivery identifier (idempotency key on receiver side) |
| `webhook-timestamp` | Unix seconds as decimal string |
| `webhook-signature` | One or more space-separated `v1,<base64>` HMAC-SHA256 tokens |

These names are the **standard-webhooks neutral names** defined in
`standard-webhooks/standard-webhooks spec/standard-webhooks.md`
(`webhook-id` / `webhook-timestamp` / `webhook-signature`), NOT the
Svix-branded `svix-id` / `svix-timestamp` / `svix-signature`.

Rationale: GoCell is the **sender** defining its own outbound protocol. Embedding
a third-party vendor name (`svix-*`) in GoCell's own protocol header names would
be inappropriate — it implies a dependency on Svix's naming that GoCell does not
have. The signed content format (concatenated `"{id}.{timestamp}.{body}"`),
HMAC-SHA256, and the `v1,<base64>` token format are identical to the signer
implementation (`kernel/webhook/signer.go`) and are Svix/standard-webhooks
aligned; only the header names are GoCell's deliberate choice.

The sole writer of these headers in the codebase is
`(webhook.SignedHeaders).Apply` (`kernel/webhook/webhook.go`), locked downstream
by archtest `WEBHOOK-SIGNER-FUNNEL-01/A1`. No callsite may call `Header.Set` on
these header name constants directly.

> **Amendment 2026-06-02 (review #1455).** F9: the `WEBHOOK-SIGNER-FUNNEL-01`
> scan scope used exact package-path matching, which silently excluded the
> `runtime/webhook/dispatch` subpackage (the consumer layer that actually calls
> `SignedHeaders.Apply`). The scope is now subtree-matched (`base` or `base+"/"`),
> so the dispatch subpackage and any future subpackage are covered.
>
> **Amendment 2026-06-07 (#1492 + #1733 F1 — provenance gap CLOSED).** F5 (the open
> hardening noted in the 2026-06-02 amendment) is **resolved**. Previously `Apply`
> lived on the public `webhook.Headers` struct with exported fields, so a caller
> could construct `Headers{Signature: …}.Apply(h)` outside `Signer.Sign` — the
> funnel locked only the *write site*, not *provenance*. #1492 split the type: the
> inbound parse DTO stays `webhook.Headers` (untrusted by design — a forged inbound
> Headers just fails `Verify`), while the OUTBOUND value `Apply` writes is the
> sealed `webhook.SignedHeaders`. A POPULATED outside-package literal
> (`SignedHeaders{signature: …}`) is a Go compile error (unexported fields), but
> unexported fields alone do NOT stop a ZERO-value construction
> (`var h webhook.SignedHeaders`) — the residual gap **review #1733 F1** caught
> (the original "closed" claim was itself an overclaim). The closure is an
> unexported `valid` provenance flag that ONLY `Signer.Sign` sets, with `Apply`
> fail-closing (writing nothing) when `valid==false`; external code can set neither
> a populated literal nor `valid`, so only a Sign-produced SignedHeaders writes to
> the wire. The field set (incl. `valid`) is reflect-frozen by
> `WEBHOOK-SIGNER-FUNNEL-01/A2`. **Upstream is now genuinely Hard (external)**; the
> only residual is the package-internal holder axis (`SignedHeaders{valid: true}`
> in-package), the permanent Go ceiling shared with #851/#893/#1282/#1375.

### D5 — Per-attempt wall-clock delays via broker-native delayed re-delivery (amended 2026-06-10, #1458)

> **Amendment (2026-06-10, #1458): the PR-5 gap described at the end of this
> section is now CLOSED.** Webhook dispatch honours the full Svix per-attempt
> schedule on **both** `outbox.Subscriber` implementations — no broker plugin,
> durable across restarts on rabbitmq.

**Mechanism.** `outbox.Subscription.BrokerDelaySchedule []time.Duration` is the
transport-neutral carrier (sourced from `DefaultSvixSchedule().Delays()` by the
webhook-dispatch bootstrap drain via `cell.WithSubscriptionBrokerDelaySchedule`,
so adapters never import `kernel/webhook`):

- **rabbitmq (TTL+DLX delay-tier queues)**: on a transient `Requeue` for a
  delayed subscription, the subscriber republishes the delivery to a per-tier
  queue `{queue}.delay.{i}` (`x-message-ttl = schedule[i]`, no consumer) that
  dead-letters back to the dispatch exchange on expiry. The attempt counter
  rides the `x-webhook-attempt` AMQP header; once `attempt > len(schedule)` the
  entry is `Nack(requeue=false)` → DLX. Delays are held in **durable queues**, so
  the ~40 h envelope survives process and broker-node restarts (unlike the
  community `x-delayed-message` plugin, which holds delayed messages in node
  memory — explicitly rejected).
- **in-memory bus**: `handleWithRetry` uses `len(schedule)+1` deliveries as the
  budget and waits `schedule[attempt]` via the injected clock between attempts;
  exhaustion routes to the dead-letter slice.
- **`ConsumerBase`**: a subscription carrying `BrokerDelaySchedule` runs in
  single-attempt pass-through mode (`brokerDelayPassthrough`). The handler's
  verdict (Ack/Requeue/Reject) is returned verbatim and the transport owns the
  retry budget, the per-attempt delay, and the final DLX routing. ConsumerBase
  must NOT stack its own exponential backoff nor convert the transient `Requeue`
  into a retry-exhausted `Reject` — doing so would dead-letter the entry on the
  first failure and bypass the schedule.
- **Cross-transport guard**: the shared `outboxtest`
  `DelayedRedeliveryHonorsSchedule` conformance feature is mandatory for every
  requeue-supporting Subscriber (it passes a custom short schedule and asserts
  the exact delivery count + cumulative delay), so a transport that ignored
  `BrokerDelaySchedule` fails CI rather than silently degrading to immediate
  retry. The new `Subscription` field is locked by `SUBSCRIPTION-FIELDS-FROZEN-01`.

**Threat / safety re-evaluation.** The 30 s ConsumerBase ceiling no longer
applies to webhook dispatch; the full ~40 h Svix envelope is realized.
At-least-once delivery is unchanged: the delay round-trip **releases (does not
commit)** the idempotency receipt, so the redelivered same-`entry.ID` message
re-claims and the handler re-runs — downstream handlers must remain idempotent
(already required). No new wire field or PII surface is introduced;
`x-webhook-attempt` is internal broker metadata, never part of the signed
payload. `RetrySchedule.DelayFor` / `Delays()` remain the single source of the
tier values.

---

**Historical context (PR-5).** PR-5 shipped `DefaultSvixSchedule` as the
canonical default and `RetrySchedule.DelayFor`/`Delays()` as the documented
seam, but did **not** wire per-attempt delays at runtime: the dispatch consumer
rode the shared `kernel/outbox.ConsumerBase` whose in-process exponential
backoff is capped at 30 s and cannot hold a 10 h delay across restarts. That
gap was deliberate and tracked at #1458 — now resolved by the mechanism above.

## Consequences

- **Runtime dispatch consumer** (`runtime/webhook/dispatch`) sources
  `DefaultSvixSchedule().Delays()` onto `outbox.Subscription.BrokerDelaySchedule`;
  the Subscriber honours the per-attempt schedule via broker-native delayed
  re-delivery (#1458, see D5). `ConsumerBase` runs delayed subscriptions in
  single-attempt pass-through mode, so its 30 s backoff ceiling does not apply to
  webhook dispatch.
- **Permanent failures** (amended 2026-06-02) are now only: SSRF-blocked target,
  a selector that signals `ErrWebhookPermanentFailure` (no subscription
  configured), signing failure, delivery-id derivation failure, and URL
  validation failure (including embedded userinfo). These route to DLX via
  `outbox.Reject` at preflight/transport; operators must inspect the DLX queue
  and fix the registration before re-enqueuing. **Non-2xx delivery responses are
  no longer permanent** — they are retried (review #1455 F3).
- **Transient failures** — every non-2xx delivery response (3xx/4xx/5xx) and all
  non-SSRF transport errors (DNS resolution failure, timeout, connection
  refused) — are retried on the Svix per-attempt schedule (#1458); on budget
  exhaustion (`attempt > len(schedule)`) they are escalated to Reject → DLX. A
  generic target-selector error is also transient (review #1455 F2): a
  store/config hiccup retries rather than dead-lettering.
- **Wire protocol**: receivers that verify GoCell outbound webhooks must use the
  `webhook-id` / `webhook-timestamp` / `webhook-signature` header names, not
  `svix-*`. GoCell's example receiver (`kernel/webhook.Verifier`) already uses
  these names.
- **Broker-delay (resolved, gh #1458)**: deliveries now honour the full ~40 h
  Svix envelope (5 s → 5 min → 30 min → 2 h → 5 h → 10 h → 10 h) via TTL+DLX
  delay-tier queues (rabbitmq) and the clock-timed schedule (in-memory bus),
  durable across restarts. See D5 for the mechanism and threat re-evaluation.

## References

- Svix retry table: `https://docs.svix.com/retries`
- standard-webhooks specification: `standard-webhooks/standard-webhooks
  spec/standard-webhooks.md` (retry guidance; header names `webhook-id` /
  `webhook-timestamp` / `webhook-signature`)
- Convoy webhook delivery docs: `https://www.getconvoy.io/docs`
- ADR `202605312300-1159-adr-webhook-ssrf-policy.md` (SSRF guard; `SafePolicy`
  and `ErrWebhookSSRFBlocked` referenced in D2)
- ADR `202605291200-adr-webhook-signing-algorithm.md` (HMAC-SHA256 algorithm;
  signed content format; `v1,<base64>` token form)
- ref: svix/svix-webhooks `RetrySchedule` default delays
- ref: standard-webhooks/standard-webhooks `spec/standard-webhooks.md` timeout
  and retry guidance
- ref: getconvoy/convoy `internal/config` delivery timeout default
