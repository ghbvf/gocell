// Package webhook is the kernel-level pure-computation core for GoCell's
// bidirectional webhook capability (KERNEL-WEBHOOK-01): inbound receiver
// verification and outbound dispatcher signing. It depends only on the
// standard library + pkg/errcode + pkg/redaction + pkg/panicregister +
// kernel/clock (kernel/ layering rule — no runtime/ adapters/ cells/).
//
// Scope: L0 (signature compute + verify are pure functions of input bytes).
// The HTTP receiver middleware and the outbox dispatcher consumer that wire
// these primitives into request/delivery flows live in runtime/webhook
// (PR-3 / PR-5).
//
// # Core surface
//
//   - [Algorithm] — signing algorithm marker; the only legal value is
//     [AlgorithmHMACSHA256]. [Algorithm.Validate] rejects everything else with
//     [errcode.ErrWebhookAlgorithmUnsupported].
//   - [SourceID] / [DeliveryID] — typed string newtypes with [NewSourceID] /
//     [NewDeliveryID] validators (+ Must variants). Mirror the
//     kernel/healthz.ProbeName funnel shape.
//   - [Source] — an opaque (id, secret) pair. Its secret field is unexported
//     with no getter, and [Source.LogValue] redacts it, so a webhook secret
//     can never reach slog/spans by accident — see the secret-leak defenses
//     below.
//   - [Headers] — the wire signature headers (delivery id, timestamp,
//     signature) produced by a [Signer] and consumed by a [Verifier].
//   - [Signer] / [Verifier] — sealed interfaces (unexported sealed() marker)
//     so package-external implementations are a compile error. The sole
//     in-package implementations are the HMAC-SHA256 signer/verifier built by
//     [NewHMACSigner] / [NewHMACVerifier].
//   - [SourceStore] / [SourceRegistry] — secret lookup interface + in-memory
//     implementation (externally replaceable; persistent stores are a
//     follow-up).
//
// # Signing scheme (Svix-aligned)
//
// The signed content is the byte concatenation
// "{deliveryID}.{unixTimestamp}.{body}", MAC'd with HMAC-SHA256 over the
// source secret. The signature header value is "v1,<base64(mac)>"; a
// space-separated list of such tokens is accepted on verify so a sender can
// rotate secrets without a flag day. Verification recomputes the MAC and uses
// crypto/hmac.Equal (constant time) against each presented token, after a
// bidirectional timestamp-tolerance window check (default ±5min).
//
// ref: svix/svix-webhooks go/webhook.go@main
// ref: stripe/stripe-go webhook/client.go@master
//
// # AI-robust funnel: WEBHOOK-HMAC-FUNNEL-01
//
// Enforced by tools/archtest/webhook_hmac_funnel_test.go. Ratings:
//
//   - A1 (downstream Hard): crypto/hmac.New has exactly one callsite —
//     computeMAC in signer.go. Any other hmac.New in this package fails CI.
//     This also closes the package-internal upstream blind spot: any new struct
//     that wants to sign MUST call hmac.New, which is allowlisted to signer.go.
//   - A2 (downstream Hard): signature comparison may only use crypto/hmac.Equal
//     or crypto/subtle.ConstantTimeCompare; bytes.Equal / == over signature
//     bytes fails CI (constant-time invariant as an AST lock, not a flaky
//     timing test).
//   - A3 (upstream Hard external / Medium internal): [Signer] / [Verifier]
//     carry an unexported sealed() marker, so external implementations are a
//     compile error (Hard). Package-internal new holders are not blocked by
//     sealing (Medium) — covered transitively by A1. The explicit Hard-ization
//     of the internal axis (unexported method-set interface + private
//     construction, per the SPAN-SETATTR-HOLDER-SEAL precedent) is tracked in
//     gh issue #1243; this godoc names it per ai-robust.md §Funnel 双向锁评级.
//
// # Secret-leak defenses (Source.Secret)
//
// pkg/redaction masks the new key set webhook_secret / x_signature /
// x_hub_signature / x_webhook_signature / svix_signature / hmac_key, but that
// only covers string-key paths. [Source]'s secret is raw bytes, so a
// slog.Any("source", src) would otherwise leak it. Two lines of defense:
// (1) [Source.LogValue] renders the secret as redaction.Mask (type-system
// level — every slog of a Source is auto-redacted); (2) archtest blind-spot
// self-check TestWebhookFunnel_NoRawSecretSlog bans slog of a raw Source /
// Source secret in this package.
package webhook
