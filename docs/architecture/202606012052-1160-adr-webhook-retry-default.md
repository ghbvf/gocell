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
> **Amendment 2026-06-07 (#1492 — provenance gap CLOSED).** F5 (the open hardening
> noted in the 2026-06-02 amendment) is **resolved**. Previously `Apply` lived on
> the public `webhook.Headers` struct with exported fields, so a caller could
> construct `Headers{Signature: …}.Apply(h)` outside `Signer.Sign` — the funnel
> locked only the *write site*, not *provenance*. #1492 split the type: the inbound
> parse DTO stays `webhook.Headers` (untrusted by design — a forged inbound Headers
> just fails `Verify`), while the OUTBOUND value `Apply` writes is the new sealed
> `webhook.SignedHeaders` (unexported fields, no exported constructor, produced
> only by `Signer.Sign` — sealed construction). Outside-package literal forge is
> now a Go compile error; the field set is reflect-frozen by
> `WEBHOOK-SIGNER-FUNNEL-01/A2`. **Upstream is now Hard (external)**; the only
> residual is the package-internal holder axis (a same-package `SignedHeaders{…}`
> literal), the permanent Go ceiling shared with #851/#893/#1282/#1375.

### D5 — Schedule is the canonical default seam; per-attempt wall-clock delays and RetryCount are NOT yet wired (explicit boundary + tracked follow-up)

This is the most important honesty section of this ADR.

**PR-5 does NOT wire per-attempt delays or a per-dispatcher RetryCount at
runtime.** Specifically:

- `Dispatcher` has no schedule field.
- `runtime/webhook/dispatch.BuildConsumers` never sets
  `ConsumerBaseConfig.RetryCount` from the schedule.
- The global `kernel/outbox.ConsumerBase` cannot accept a per-dispatcher retry
  count; the dispatch consumer rides the shared ConsumerBase with its default
  exponential backoff capped at 30 s.

The exact per-attempt wall-clock delays (5 s → 5 min → 30 min → 2 h → 5 h →
10 h → 10 h, totalling approximately 40 h) are therefore **NOT honoured at
runtime**. Honouring the full Svix timeline requires broker-level delayed
re-delivery (e.g. RabbitMQ `x-delayed-message`, a delayed queue, or a
scheduler) that GoCell does not yet wire for the dispatch consumer.

This gap is **deliberate and explicitly tracked**, not silent. `DefaultSvixSchedule`
is retained as the canonical default because honouring the full Svix timeline
is a real future need: `RetrySchedule.DelayFor(n int)` is exactly the per-retry
delay seam the broker-delay wiring will consume. Removing the schedule now and
re-adding it later would be churn with no benefit.

The follow-up work — broker-delay long-schedule wiring for the dispatch consumer
(extend `ConsumerBase` per-attempt delay, or replace with a delay-aware relay
that calls `DelayFor(attemptNumber)`) — is tracked at **gh #1458**.

The `RetrySchedule` godoc (`kernel/webhook/retry.go`) also states this gap
explicitly.

## Consequences

- **Runtime dispatch consumer** (`runtime/webhook/dispatch`) rides the shared
  `ConsumerBase` with its default exponential back-off (capped at 30 s per
  retry interval). PR-5 does NOT set a per-dispatcher `ConsumerBaseConfig.RetryCount`
  from the schedule — `DefaultSvixSchedule` is the canonical default and the
  seam for the broker-delay follow-up (gh #1458), not yet runtime-honored.
- **Permanent failures** (amended 2026-06-02) are now only: SSRF-blocked target,
  a selector that signals `ErrWebhookPermanentFailure` (no subscription
  configured), signing failure, delivery-id derivation failure, and URL
  validation failure (including embedded userinfo). These route to DLX via
  `outbox.Reject` at preflight/transport; operators must inspect the DLX queue
  and fix the registration before re-enqueuing. **Non-2xx delivery responses are
  no longer permanent** — they are retried (review #1455 F3).
- **Transient failures** — every non-2xx delivery response (3xx/4xx/5xx) and all
  non-SSRF transport errors (DNS resolution failure, timeout, connection
  refused) — trigger `ConsumerBase` retry with default backoff; on budget
  exhaustion they are escalated to Reject → DLX. A generic target-selector error
  is also transient (review #1455 F2): a store/config hiccup retries rather than
  dead-lettering.
- **Wire protocol**: receivers that verify GoCell outbound webhooks must use the
  `webhook-id` / `webhook-timestamp` / `webhook-signature` header names, not
  `svix-*`. GoCell's example receiver (`kernel/webhook.Verifier`) already uses
  these names.
- **Broker-delay gap (tracked gh #1458)**: until the follow-up lands, deliveries
  exhaust their retry budget faster than the 40 h Svix envelope because
  ConsumerBase back-off is capped at 30 s per interval rather than honouring
  the schedule's 2 h / 5 h / 10 h steps. `RetrySchedule.DelayFor` is the seam
  that follow-up will consume.

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
