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
| Replay of a captured valid delivery | bidirectional timestamp tolerance window | ✅ (bounded to ±tolerance; full idempotency dedupe is the receiver's Claimer, PR-3/PR-6) |
| Timing side-channel on signature compare | `hmac.Equal` constant-time; A2 AST lock | ✅ |
| Secret leak via logs/spans/error text | unexported secret + no getter + `Source.LogValue` mask + redaction key set + archtest B6 | ✅ |
| Secret mutation after construction | `NewSource` defensive copy | ✅ |
| Body-shifting between signed-content fields | MAC covers the whole string; fields never parsed back | ✅ (no forgery without the secret) |

## Consequences

- The receiver (PR-3) and dispatcher (PR-5) consume `Verifier`/`Signer` as the
  only signing surface; they cannot introduce a second algorithm.
- Secret rotation is supported on the verify side (multi-token header); persistent
  multi-secret sources are a follow-up.

## References

- Svix manual verification: https://docs.svix.com/receiving/verifying-payloads/how-manual
- Stripe webhook signatures: https://github.com/stripe/stripe-go/blob/master/webhook/client.go
- ADR `202605051730-adr-errcode-message-pii-safety.md` (Message/Details/Internal redaction)
- ADR `202604242030-adr-kernel-wrapper-contract-observability.md` §8 (span/error redaction)
- ai-robust.md §Funnel 双向锁评级; gh #1243 (upstream Hard-ization), #851 (precedent)
