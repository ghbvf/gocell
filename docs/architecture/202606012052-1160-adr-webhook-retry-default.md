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
`DefaultSvixSchedule().MaxRetries()` is consumed by the runtime dispatch
consumer to size `ConsumerBaseConfig.RetryCount` — the schedule is a real
parameter, not a dead abstraction.

### D2 — HTTP status code → `outbox.HandleResult` mapping (standard-webhooks aligned)

The single classification function is `kernel/webhook/retry.go::Classify(statusCode
int, transportErr error) outbox.Disposition`.

| Outcome | Disposition | Rationale |
|---------|-------------|-----------|
| 2xx | `DispositionAck` (Ack) | Delivery succeeded. |
| 5xx | `DispositionRequeue` (transient) | Server-side fault; the request itself is well-formed, a retry after back-off is appropriate. Aligned with Svix, Convoy, and the standard-webhooks guidance that senders should retry on server errors. |
| 408 Request Timeout | `DispositionRequeue` (transient) | The server timed out processing the request; back off and retry is the spec-recommended sender behaviour. |
| 429 Too Many Requests | `DispositionRequeue` (transient) | Throttled; the standard-webhooks spec asks senders to respect rate limits and retry. |
| 3xx | `DispositionReject` (permanent → DLX) | Redirects are denied at the transport layer by `SafePolicy.DenyRedirect` and surface as an SSRF transport error (see below); they never reach status classification in practice. If one did reach this branch, a redirect indicates the target endpoint has moved and won't become valid on retry without operator intervention. |
| Other 4xx (400, 401, 403, 404, 410, …) | `DispositionReject` (permanent → DLX) | The request itself is malformed, unauthorized, forbidden, or targeting a non-existent resource. Retrying an identical request against the same endpoint will not produce a different result. Svix and Convoy treat the 4xx set (excluding 408/429) as permanent. |
| SSRF-blocked (`ErrWebhookSSRFBlocked`) | `DispositionReject` (permanent → DLX) | A misconfigured or hostile target URL (bad scheme, blocked dial, denied redirect). The target will not become safe on retry; operator intervention is required to correct the webhook endpoint registration. |
| Transport timeout / connection refused / network error (non-SSRF) | `DispositionRequeue` (transient) | Transient connectivity fault; retry after back-off. |

**Note on 3xx**: in normal operation 3xx responses never reach the `Classify`
switch because `SafePolicy.DenyRedirect` (wired as the `*http.Client`
`CheckRedirect`) returns an error on the first redirect, which surfaces as a
transport error carrying `ErrWebhookSSRFBlocked` → `DispositionReject`.
The 3xx row in the table describes the fallback behaviour if that layer were
absent or bypassed; it is included for completeness and auditability.

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

The sole writer of these headers in the codebase is `(webhook.Headers).Apply`
(`kernel/webhook/webhook.go`), locked downstream by archtest
`WEBHOOK-SIGNER-FUNNEL-01`. No callsite may call `Header.Set` on these header
name constants directly.

### D5 — Schedule drives retry budget; exact per-attempt wall-clock delays are NOT yet honoured (explicit boundary + tracked follow-up)

This is the most important honesty section of this ADR.

`DefaultSvixSchedule().MaxRetries()` sizes `ConsumerBaseConfig.RetryCount` in
the runtime dispatch consumer. The schedule is **genuinely consumed** at the
constructor site; it is not a dead abstraction.

**However**, the exact per-attempt wall-clock delays (5 s → 5 min → 30 min →
2 h → 5 h → 10 h → 10 h, totalling approximately 40 h) are **NOT yet honoured
at runtime**. `kernel/outbox.ConsumerBase` uses in-process exponential backoff
with a hard cap at 30 s per retry interval. It has no mechanism to hold a 10 h
delay across process restarts — it would require broker-level delayed re-delivery
(e.g. RabbitMQ `x-delayed-message`, a delayed queue, or a scheduler) that
GoCell does not yet wire for the dispatch consumer.

This gap is **deliberate and explicitly tracked**, not silent. The schedule is
retained in the implementation because honouring the full Svix timeline is a
real future need: the `RetrySchedule` type and `DelayFor(n int)` method
provide exactly the per-retry delay seam that the broker-delay wiring will
consume. Removing the schedule now and re-adding it later would be churn with
no benefit.

The follow-up work — broker-delay long-schedule wiring for the dispatch consumer
(extend `ConsumerBase` per-attempt delay, or replace with a delay-aware relay
that calls `DelayFor(attemptNumber)`) — is registered as a backlog item
(label: `cap-webhook`, `pri-p2`).

The `RetrySchedule` godoc (`kernel/webhook/retry.go`) also states this gap
explicitly:

> PR-5 scope: the schedule is the canonical default and drives the consumer's
> retry budget (MaxRetries == len(delays)); the exact per-attempt wall-clock
> delays are NOT yet honoured by the in-process ConsumerBase backoff (capped at
> 30s). Honouring the full Svix timeline (up to ~40h) needs broker-delay
> support and is a tracked follow-up (see ADR webhook-retry-default). The
> schedule is retained because that follow-up is a real future need, not a dead
> abstraction.

## Consequences

- **Runtime dispatch consumer** (`runtime/webhook/dispatch`) calls
  `dispatcher.Schedule().MaxRetries()` to set `ConsumerBaseConfig.RetryCount`
  to 7; the consumer inherits `ConsumerBase` exponential back-off (capped at
  30 s) for all seven retries.
- **Permanent failures** (SSRF-blocked, non-2xx 4xx excluding 408/429, selector
  error, signing failure, URL validation failure) route to DLX via
  `outbox.Reject`; operators must inspect the DLX queue and fix the registration
  before re-enqueuing.
- **Transient failures** (5xx, 408, 429, transport errors) trigger
  `ConsumerBase` retry up to `MaxRetries` times; on budget exhaustion they are
  escalated to Reject → DLX.
- **Wire protocol**: receivers that verify GoCell outbound webhooks must use the
  `webhook-id` / `webhook-timestamp` / `webhook-signature` header names, not
  `svix-*`. GoCell's example receiver (`kernel/webhook.Verifier`) already uses
  these names.
- **Broker-delay gap**: until the tracked follow-up lands, high-retry-count
  deliveries will exhaust their budget faster than the 40 h Svix envelope
  because ConsumerBase back-off is capped at 30 s per interval rather than
  honouring the schedule's 2 h / 5 h / 10 h steps.

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
