// Package webhook is the kernel-level pure-computation core for GoCell's
// bidirectional webhook capability (KERNEL-WEBHOOK-01): inbound receiver
// verification and outbound dispatcher signing. It depends only on the
// standard library + pkg/errcode + pkg/redaction + kernel/clock in
// production (kernel/ layering rule — no runtime/ adapters/ cells/);
// pkg/panicregister is a test-only dependency of webhook_helpers_test.go.
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
//     [NewDeliveryID] validators. Mirror the kernel/healthz.ProbeName funnel
//     shape. (Panic-on-error Must* constructors are test-only fixtures in
//     webhook_helpers_test.go, not part of the production surface.)
//   - [Source] — an opaque (id, secret) pair. Its secret field is unexported
//     with no getter, and [Source.LogValue] redacts it, so a webhook secret
//     can never reach slog/spans by accident — see the secret-leak defenses
//     below.
//   - [Headers] — the INBOUND signature-header DTO (delivery id, timestamp,
//     signature) that a [Verifier] consumes; the receiver builds it from raw
//     request headers (untrusted input), so its fields are exported.
//   - [SignedHeaders] — the OUTBOUND, provenance-sealed counterpart that
//     [Signer.Sign] produces and [SignedHeaders.Apply] writes to the wire. Its
//     fields are unexported, incl. a `valid` provenance flag only Sign sets, and
//     Apply fail-closes on a zero value — so only a Sign-produced value reaches
//     the wire (WEBHOOK-SIGNER-FUNNEL-01 upstream Hard external; #1492 + #1733 F1).
//   - [Signer] / [Verifier] — sealed interfaces (unexported sealed() marker)
//     so package-external implementations are a compile error. The sole
//     in-package implementations are the HMAC-SHA256 signer/verifier built by
//     [NewHMACSigner] / [NewHMACVerifier]; [Signer.Sign] returns a [SignedHeaders].
//   - [SourceStore] / [SourceRegistry] — secret lookup interface + in-memory
//     implementation (externally replaceable). A persistent encrypted backing
//     store (adapters/postgres webhook_sources, wired by cellmodules/webhooksource)
//     loads its secrets through the sealed crypto funnel [Source.Encrypt] /
//     [NewSourceFromCiphertext] at boot and serves them from a SourceRegistry
//     snapshot — the plaintext secret never leaves this package.
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
//   - A1 (downstream Hard): crypto/hmac.New has exactly one callsite (by go/types
//     FullName) — computeMAC. Any other hmac.New in this package fails CI.
//   - A2 (downstream Hard): signature comparison may only use crypto/hmac.Equal
//     or crypto/subtle.ConstantTimeCompare; bytes.Equal / == over signature
//     bytes fails CI (constant-time invariant as an AST lock, not a flaky
//     timing test).
//   - A3 (upstream Hard external / Medium internal): [Signer] / [Verifier]
//     carry an unexported sealed() marker, so external implementations are a
//     compile error (Hard external). Package-internal new holders are NOT blocked
//     by sealing, and A1 does NOT transitively cover them (A1 locks only
//     crypto/hmac.New, not reuse of the package-level computeMAC helper) — that
//     internal axis is covered by A4. True type-system Hard for it is a permanent
//     Go ceiling (same family as #851/#893/#1282/#1375), tracked won't-do at gh
//     #1243.
//   - A4 (Medium, internal axis; #1243): every USE of the package-internal
//     computeMAC symbol (direct/parenthesized call or function-value capture) must
//     have an enclosing func ∈ {hmacSigner.Sign, hmacVerifier.Verify} — the actual
//     enforcement of "only the sanctioned signer/verifier may compute a MAC".
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
