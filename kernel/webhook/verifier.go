package webhook

import (
	"crypto/hmac"
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// defaultTolerance is the bidirectional timestamp window within which a signed
// delivery is accepted (replay defense). ±5min matches Stripe / Svix defaults.
const defaultTolerance = 5 * time.Minute

// MsgSignatureVerificationFailed is the single, generic wire message returned by
// every inbound-webhook authentication failure that resolves to HTTP 401 with
// [errcode.ErrWebhookInvalidSignature]: an unregistered/unknown source (receiver
// SourceStore miss) and a non-matching HMAC signature (this verifier) MUST be
// byte-identical on the wire so the receive path is not a source-ID enumeration
// oracle (OWASP Authentication Cheat Sheet §Generic Error Messages;
// WEBHOOK-HMAC-FUNNEL-01 F8/F9). The differentiating reason lives in each
// caller's WithInternal attribute (server-side slog only, never on the wire).
//
// This is a shared-const convention (Soft): both 401 emit points reference it,
// but a future third path could still construct a different literal. The narrow
// exploitability (source IDs are route-bound, not attacker-supplied) makes an
// archtest disproportionate; the const + this godoc + the errcode.go contract
// note are the single source of the required wire text.
//
// ref: stripe/stripe-go webhook/client.go@master / svix/svix-webhooks
// go/webhook.go@main — neither performs a source lookup (secret is caller-
// supplied), so this oracle surface is GoCell-specific; OWASP provides the
// governing principle.
const MsgSignatureVerificationFailed = "webhook: signature verification failed"

// Verifier checks the signature [Headers] of an inbound webhook against a
// [Source]'s secret. The interface is sealed (unexported sealed() marker): the
// sole implementation is the HMAC-SHA256 verifier from [NewHMACVerifier]
// (WEBHOOK-HMAC-FUNNEL-01/A3 upstream).
type Verifier interface {
	// Verify returns nil when rawBody, headers, and source produce a matching
	// HMAC within the tolerance window; otherwise it returns a typed
	// *errcode.Error sentinel (ErrWebhookInvalidHeader / TimestampExpired /
	// InvalidSignature / ConfigInvalid).
	Verify(rawBody []byte, headers Headers, source Source) error
	sealed()
}

type hmacVerifier struct {
	clk       clock.Clock
	tolerance time.Duration
}

// VerifierOption configures [NewHMACVerifier]; use [WithTolerance] to override defaults.
type VerifierOption func(*hmacVerifier)

// WithTolerance overrides the default ±5min timestamp window. A non-positive
// value is rejected at construction.
func WithTolerance(d time.Duration) VerifierOption {
	return func(v *hmacVerifier) { v.tolerance = d }
}

// NewHMACVerifier returns an HMAC-SHA256 [Verifier]. clk is the mandatory
// positional clock used for the timestamp-tolerance window
// (CLOCK-POSITIONAL-INJECTION-01).
func NewHMACVerifier(clk clock.Clock, opts ...VerifierOption) (Verifier, error) {
	clock.MustHaveClock(clk, "webhook.NewHMACVerifier")
	v := &hmacVerifier{clk: clk, tolerance: defaultTolerance}
	for _, o := range opts {
		if o != nil {
			o(v)
		}
	}
	if v.tolerance <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: verifier tolerance must be positive")
	}
	return v, nil
}

func (*hmacVerifier) sealed() {}

func (v *hmacVerifier) Verify(rawBody []byte, headers Headers, source Source) error {
	if len(source.secret) == 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: verify requires a source with a non-empty secret")
	}
	if err := headers.DeliveryID.Validate(); err != nil {
		return err
	}
	if err := v.validateTimestamp(headers.Timestamp); err != nil {
		return err
	}
	expected := computeMAC(source.secret, headers.DeliveryID, headers.Timestamp, rawBody)
	if !matchAnySignature(headers.Signature, expected) {
		// sourceId goes to Internal (server-side slog), not Details: the wire
		// 401 must be uniform with the unknown-source case so the receive path
		// is not a source-ID enumeration oracle (WEBHOOK-HMAC-FUNNEL-01 F8/F9).
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrWebhookInvalidSignature,
			MsgSignatureVerificationFailed,
			errcode.WithInternal(
				errcode.InternalAttr("source_id", string(source.id)),
				errcode.InternalAttr("reason", "signature mismatch")))
	}
	return nil
}

// validateTimestamp parses the unix-seconds timestamp header and enforces the
// bidirectional tolerance window.
func (v *hmacVerifier) validateTimestamp(timestamp string) error {
	tsInt, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrWebhookInvalidHeader,
			"webhook: timestamp header is not a unix-seconds integer",
			errcode.WithInternal(errcode.InternalAttr("rawTimestamp", timestamp)))
	}
	skew := v.clk.Now().Sub(time.Unix(tsInt, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > v.tolerance {
		// skewSeconds is server-side only: exposing the exact skew in wire
		// details would let an attacker calibrate replay timing. toleranceSeconds
		// is safe to expose (it is the configured window, not a runtime secret).
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrWebhookTimestampExpired,
			"webhook: signature timestamp is outside the tolerance window",
			errcode.WithDetails(
				errcode.PublicInt("toleranceSeconds", int64(v.tolerance/time.Second)),
			),
			errcode.WithInternal(errcode.InternalAttr("skewSeconds", int64(skew/time.Second))))
	}
	return nil
}

// matchAnySignature reports whether any space-separated "v1,<base64>" token in
// header decodes to a MAC equal to expected. Comparison uses crypto/hmac.Equal
// (constant time) — WEBHOOK-HMAC-FUNNEL-01/A2. Multiple tokens support a
// sender rotating secrets without a flag day.
func matchAnySignature(header string, expected []byte) bool {
	for _, token := range strings.Fields(header) {
		scheme, encoded, ok := strings.Cut(token, signatureSchemeSep)
		if !ok || scheme != signatureSchemeV1 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		if hmac.Equal(expected, raw) {
			return true
		}
	}
	return false
}
