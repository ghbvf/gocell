# ADR: Webhook signing algorithm — HMAC-SHA256 only, sealed funnel

- Status: Accepted
- Date: 2026-05-29
- Context: KERNEL-WEBHOOK-01 PR-1 (#1156)
- Supersedes / amends: none (greenfield)

## Context

`kernel/webhook` is the pure-computation core for GoCell's bidirectional
webhook capability (inbound receiver verification + outbound dispatcher
signing). The signing algorithm and the wire signature format are decided here,
in the same PR that implements them, rather than deferred — the signer/verifier
code IS the decision, and a dangling forward reference to a later PR's ADR would
leave the security-critical choice undocumented at the point of implementation.

## Decision

1. **HMAC-SHA256 is the only legal signing algorithm.** `webhook.Algorithm` has
   exactly one valid value, `AlgorithmHMACSHA256`; `Algorithm.Validate` rejects
   every other value (including any SHA-1 form) with
   `errcode.ErrWebhookAlgorithmUnsupported`. There is no algorithm-negotiation
   path and no downgrade path.

2. **Signed content is Svix-aligned:** the byte concatenation
   `"{deliveryID}.{unixTimestamp}.{body}"`, MAC'd with HMAC-SHA256 over the
   source secret. The signature header value is `"v1,<base64(mac)>"`. A
   space-separated list of such tokens is accepted on verify so a sender can
   rotate secrets without a flag day; the receiver accepts the delivery if any
   presented token's decoded MAC matches the recomputed MAC.

3. **Constant-time comparison is mandatory.** Verification compares the raw MAC
   bytes with `crypto/hmac.Equal`. `bytes.Equal` / `==` over signature bytes is
   forbidden.

4. **Replay defense via a bidirectional timestamp window.** The verifier rejects
   a delivery whose signed timestamp is more than the tolerance (default ±5min)
   from the verifier's clock, in either direction, with
   `errcode.ErrWebhookTimestampExpired`.

5. **Secrets never reach logs/spans.** `webhook.Source` stores the secret in an
   unexported field with no getter; `Source.LogValue` redacts it; and
   `pkg/redaction` masks the webhook signature/secret key set. See the
   secret-leak defense section below.

## Rationale / alternatives considered

- **HMAC-SHA256 vs Ed25519 / KMS signing.** HMAC-SHA256 is the de-facto industry
  standard for webhook signing (Stripe, GitHub, Svix, Slack). Asymmetric (Ed25519)
  and KMS-backed signing (Vault Transit) are deferred follow-ups — they cross the
  `adapters/` layer boundary and are not needed for the receiver/dispatcher MVP.
- **Why a fixed `v1` scheme prefix, not an algorithm name in the header.** The
  scheme tag is a version, not an algorithm selector. Keeping algorithm choice
  out of the wire prevents downgrade negotiation. A future `v2` scheme would be a
  deliberate, code-level change, not a per-request choice.
- **Why ±5min default tolerance.** Matches Stripe/Svix defaults; large enough to
  absorb normal clock skew, small enough to bound the replay window.

## AI-robust enforcement: WEBHOOK-HMAC-FUNNEL-01

Carrier: `tools/archtest/webhook_hmac_funnel_test.go`. Symbol inventory and
blind-spot list live in that test's package godoc (per ai-robust.md "落地实例与
符号清单活在代码 godoc"); this ADR records only the ratings.

| Sub-rule | Guards | Rating |
|----------|--------|--------|
| A1 | `crypto/hmac.New` has a single callsite (`computeMAC` in signer.go) | downstream **Hard** |
| A2 | signature comparison uses only `hmac.Equal`/`subtle.ConstantTimeCompare` | downstream **Hard** |
| A3 | `Signer`/`Verifier` sealed via unexported `sealed()` marker | upstream **Hard** (external) / **Medium** (package-internal) |

A1 also closes the package-internal upstream blind spot: any new in-package
struct that wants to sign MUST call `hmac.New`, which is allowlisted to
signer.go, so an unsealed internal holder cannot produce signatures undetected.

Per ai-robust.md §Funnel 双向锁评级, the A3 package-internal Medium axis has a
tracking issue for explicit Hard-ization (unexported method-set interface +
private construction, following the SPAN-SETATTR-HOLDER-SEAL #851 precedent):
**gh #1243**. The funnel godoc names this issue.

## Threat model

| Threat | Mitigation | Status |
|--------|-----------|--------|
| Forged payload (no secret) | HMAC over full signed content; `hmac.Equal` | ✅ |
| Algorithm downgrade (SHA-1) | single legal `Algorithm`; `Validate` rejects others | ✅ |
| Replay of a captured valid delivery | timestamp tolerance window (bounds replay to ±5min) + receiver Claimer two-phase dedupe (key `webhook:{sourceID}:{deliveryID}`, TTL 24h); 409 on concurrent in-flight, Release on handler panic | ✅ under normal operation; degrades to timestamp-window + at-least-once on Commit infra fault or provider-retry-window > done-TTL — handlers must be idempotent (PR-3 #1158; see Amendment §4 Degradation modes) |
| Timing side-channel on signature compare | `hmac.Equal` constant-time; A2 AST lock | ✅ |
| Secret leak via logs/spans/error text | unexported secret + no getter + `Source.LogValue` mask + redaction key set + archtest B6 | ✅ |
| Secret mutation after construction | `NewSource` defensive copy | ✅ |
| Body-shifting between signed-content fields | MAC covers the whole string; fields never parsed back | ✅ (no forgery without the secret) |
| Source enumeration (probing unknown sourceID) | unknown source and signature failure both return 401 (ErrWebhookInvalidSignature); sourceId only in server-side Internal, never in wire details | ✅ (PR-3 #1158 landed — see Amendment below) |
| Oversized body DoS | `http.MaxBytesReader` enforced before verify; 413 ErrWebhookBodyTooLarge | ✅ (PR-3 #1158 landed — see Amendment below) |

## Consequences

The receiver (PR-3, now landed as #1158) and dispatcher (PR-5) consume
`Verifier`/`Signer` as the only signing surface; they cannot introduce a second
algorithm. The receiver enforces idempotency via a built-in `kernel/idempotency.Claimer`
(two-phase Claim/Commit/Release); cell handlers do not write their own dedupe.
Secret rotation is supported on the verify side (multi-token header); persistent
multi-secret sources are a follow-up.

## Amendment 2026-05-31: receiver runtime (PR-3, #1158)

- Date: 2026-05-31
- Context: PR-3 #1158 — receiver runtime (`runtime/webhook`) landed

### 1. HTTP status code mapping (receive path)

| Condition | Status | Error sentinel |
|-----------|--------|----------------|
| Missing or malformed signature/timestamp/delivery-id header | 400 | `ErrWebhookInvalidHeader` |
| Signature verification failure | 401 | `ErrWebhookInvalidSignature` |
| Unknown sourceId (unregistered source) | 401 | `ErrWebhookInvalidSignature` (unified — see §2) |
| Timestamp outside tolerance window (replay) | 401 | `ErrWebhookTimestampExpired` |
| Body exceeds configured limit | 413 | `ErrWebhookBodyTooLarge` |
| Idempotent duplicate (ClaimDone — already processed) | 200 | — (silent success, safe for sender retry) |
| Concurrent in-flight (ClaimBusy — another goroutine holds lease) | 409 | `ErrWebhookDuplicateDelivery` |
| Handler transient error (KindUnavailable / infra fault) | 503 | framework-mapped |
| Handler permanent error | 500 | framework-mapped |

### 2. Source enumeration defense

Unknown sourceId and signature verification failure are **unified to 401**
(`ErrWebhookInvalidSignature`) on the wire. The distinction is intentionally
erased: a probe that cycles through guessed source IDs receives the same
response as a valid source ID with an invalid signature, preventing enumeration
of registered sources.

`sourceId` is recorded only via `errcode.WithInternal` (server-side slog),
never in wire `details`. This aligns with the existing §Decision 5 secret-leak
defense: the `Secret-leak` threat row in the Threat model remains ✅
(no new leak vector introduced).

### 3. Capability-token pipeline (AI-robust: Hard downstream / Medium upstream)

The receiver pipeline enforces `verify → claim → invokeHandler` ordering at the
Go type system level using unexported capability tokens:

- `verified` token is produced only by `verify()` (requires valid signature).
- `claimed` token is produced only by `claim()`, which requires a `verified`
  argument.
- `invokeHandler()` accepts only a `claimed` argument.

Skipping `verify` or `claim`, or reordering the steps, is a **compile error**
for any code in the `runtime/webhook` package. Package-external code cannot
construct `verified` or `claimed` at all (unexported types — type-system Hard
upstream for external callers).

**AI-robust rating:**

| Direction | Form | Rating |
|-----------|------|--------|
| Upstream (package-external) | `verified`/`claimed` are unexported types; package-external construction is a compile error | **Hard** |
| Upstream (package-internal) | archtest `WEBHOOK-RECEIVER-PIPELINE-01` A1 locks struct field holder set for both token types; new in-package structs holding a token are caught at CI | **Medium** |
| Downstream (callsite) | `WEBHOOK-RECEIVER-PIPELINE-01` A2 locks `invokeHandler` callsites to require a `claimed` argument; A3 locks `claim` callsites to require a `verified` argument | **Hard** |

The package-internal Medium upstream axis follows the same permanent Go-language
ceiling as `OUTBOX-ENTRY-SEALED-CONSTRUCTION-01` (#1282 won't-do). Package-internal
upstream Hard-ization is constrained by the Go-language ceiling, the same
won't-do precedent as #1282/#851; no independent tracking issue exists.

### 4. Built-in two-phase idempotency (Claimer)

The receiver runtime embeds `kernel/idempotency.Claimer` with key
`webhook:{sourceID}:{deliveryID}` and 24h TTL. The pipeline is:

```
readBody → verify signature → Claim (get lease) → invokeHandler → Commit
                                                ↘ Release (on panic / transient error)
```

**Rationale vs. Stripe/Svix/GitHub OSS approach:** OSS webhook libraries
(go-github, svix-go) leave deduplication to the application layer (e.g., Svix
docs recommend consumer-side `webhook-id + Redis/24h`). GoCell's receiver is a
framework component, not a library: embedding Claimer matches GoCell's own
event-consumer pattern (`ConsumerBase`) and delivers three structural benefits:

1. **409 on concurrent in-flight** — triggers sender back-off, preventing
   duplicate processing races.
2. **Release on handler panic** — prevents lease deadlock that would otherwise
   permanently block re-delivery.
3. **Cell handlers are dedupe-free** — cells do not write their own idempotency
   logic for incoming webhooks, consistent with the event-consumer contract.

**Threat model impact:** `Replay of a captured valid delivery` row updated from
⚠️ partial to ✅ (see Threat model table above). The ±5min timestamp window
was already present in PR-1; the Claimer adds the second layer — deliveries that
arrive within the tolerance window but are exact repeats (same `deliveryID`) are
caught by the Claimer's 24h dedupe window and returned 200 to the sender.

### 5. Body limiting

`http.MaxBytesReader` is applied to the request body **before** any read,
capping at `ReceiverSpec.MaxBodyBytes` (default configurable per receiver via
cellgen-baked `webhook.ReceiverSpec`). Any read beyond the limit returns 413
`ErrWebhookBodyTooLarge`.

Rationale vs. alternatives: `io.LimitReader` (used by go-github) silently
truncates — the truncated body would produce a signature mismatch and return
401, hiding the real cause. `http.MaxBytesReader` returns an explicit error
that the receiver maps to 413, making the limit visible to the sender.

### 6. Configuration single source (cellgen-baked ReceiverSpec)

Receiver runtime configuration (`pathPattern`, `headers`, `tolerance`,
`maxBodyBytes`) is baked into `webhook.ReceiverSpec` literals by cellgen from
`contract.yaml` at code-generation time, analogous to how event-consumer
`Topic` is baked from `contractUsages[role=subscribe]`. There is no runtime
config file read or environment variable lookup for these values; they are
compile-time constants from the cell's perspective.

This means a cell cannot accidentally use a stale or environment-specific
receiver configuration: the contract.yaml is the single source of truth,
and a mismatch between contract and runtime requires a regenerate + rebuild.

### Threat model re-evaluation (per ai-robust.md §ADR amendment 落地必查)

All rows re-evaluated against the PR-3 implementation:

| Threat | Pre-PR-3 status | Post-PR-3 status | Change rationale |
|--------|----------------|-----------------|-----------------|
| Forged payload (no secret) | ✅ | ✅ | No change. PR-3 only adds HTTP transport; HMAC core unchanged. |
| Algorithm downgrade (SHA-1) | ✅ | ✅ | No change. `Algorithm.Validate` not touched. |
| Replay of a captured valid delivery | ⚠️ partial | ✅ (best-effort) | **Upgraded, with documented degradation.** PR-3 lands the Claimer (key `webhook:{sourceID}:{deliveryID}`, 24h TTL). Timestamp window was already in PR-1; Claimer adds within-window exact-duplicate dedupe. Both layers now active. **Not absolute**: Commit infra fault or a provider retry window exceeding the 24h done-TTL drops back to timestamp-window + at-least-once for that delivery — see §4 Degradation modes. Handlers are required to be idempotent ([webhook.WebhookReceiveHandler] godoc). |
| Timing side-channel on signature compare | ✅ | ✅ | No change. `hmac.Equal` path unchanged; A2 archtest still holds. |
| Secret leak via logs/spans/error text | ✅ | ✅ | PR-3 receiver adds `sourceId` to `errcode.WithInternal` only; wire `details` is empty for 5xx by framework strip, and the 401 path carries no details either. No new leak vector. |
| Secret mutation after construction | ✅ | ✅ | `NewSource` defensive copy unchanged. |
| Body-shifting between signed-content fields | ✅ | ✅ | MAC covers whole string; receiver reads body as bytes before passing to verifier, no intermediate parse. |
| Source enumeration (new row) | n/a | ✅ | PR-3 unifies unknown-source and bad-signature to the same 401 code and message. sourceId confined to Internal. Covered by §2 above. |
| Oversized body DoS (new row) | n/a | ✅ | PR-3 applies `http.MaxBytesReader` before any body read. Covered by §5 above. |

No previously-✅ row regressed. Two new threats (source enumeration, body DoS)
are added and immediately closed by PR-3 controls.

## References

- Svix manual verification: https://docs.svix.com/receiving/verifying-payloads/how-manual
- Stripe webhook signatures: https://github.com/stripe/stripe-go/blob/master/webhook/client.go
- ADR `202605051730-adr-errcode-message-pii-safety.md` (Message/Details/Internal redaction)
- ADR `202604242030-adr-kernel-wrapper-contract-observability.md` §8 (span/error redaction)
- ai-robust.md §Funnel 双向锁评级; gh #1243 (upstream Hard-ization A3), #851 (precedent), #1282 (Go-language ceiling precedent — pipeline token package-internal upstream Medium is the permanent ceiling, same won't-do shape)
