package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"log/slog"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

const (
	// signatureSchemeV1 is the signature version token. The wire form is
	// "v1,<base64(mac)>".
	signatureSchemeV1 = "v1"
	// signatureSchemeSep separates the scheme from the encoded MAC.
	signatureSchemeSep = ","
	// signedContentSep joins the delivery id, timestamp, and body in the
	// MAC'd content "{deliveryID}.{timestamp}.{body}".
	signedContentSep = "."
)

// Signer produces a sealed [SignedHeaders] for an outbound webhook delivery. The
// interface is sealed (unexported sealed() marker): the sole implementation is
// the HMAC-SHA256 signer from [NewHMACSigner], so package-external types cannot
// satisfy Signer (WEBHOOK-HMAC-FUNNEL-01/A3 upstream).
type Signer interface {
	// Sign computes the sealed signature headers for payload at time ts under
	// deliveryID. ts is supplied by the caller (the dispatcher passes its
	// clock's Now) so the signer holds no clock.
	Sign(payload []byte, ts time.Time, deliveryID DeliveryID) (SignedHeaders, error)
	sealed()
}

type hmacSigner struct {
	source Source
}

// Compile-time assertion: *hmacSigner implements slog.LogValuer so callers
// using slog.Any("signer", s) never leak the embedded source secret.
var _ slog.LogValuer = (*hmacSigner)(nil)

// NewHMACSigner returns an HMAC-SHA256 [Signer] bound to source. It fails if
// the source has no secret (a zero-value Source from outside the package).
func NewHMACSigner(source Source) (Signer, error) {
	if len(source.secret) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook: signer requires a source with a non-empty secret")
	}
	return &hmacSigner{source: source}, nil
}

func (*hmacSigner) sealed() {}

// LogValue implements slog.LogValuer so logging a *hmacSigner never leaks the
// source secret (defense-in-depth alongside Source.LogValue).
func (s *hmacSigner) LogValue() slog.Value {
	return slog.GroupValue(slog.Any("source", s.source))
}

func (s *hmacSigner) Sign(payload []byte, ts time.Time, deliveryID DeliveryID) (SignedHeaders, error) {
	if err := deliveryID.Validate(); err != nil {
		return SignedHeaders{}, err
	}
	timestamp := strconv.FormatInt(ts.Unix(), 10)
	mac := computeMAC(s.source.secret, deliveryID, timestamp, payload)
	signature := signatureSchemeV1 + signatureSchemeSep + base64.StdEncoding.EncodeToString(mac)
	return SignedHeaders{
		deliveryID: deliveryID,
		timestamp:  timestamp,
		signature:  signature,
		valid:      true, // provenance flag — only Sign sets it; Apply fail-closes on false.
	}, nil
}

// computeMAC is the SINGLE crypto/hmac.New callsite in this package
// (WEBHOOK-HMAC-FUNNEL-01/A1). It MACs the signed content
// "{deliveryID}.{timestamp}.{body}" with HMAC-SHA256 over secret and returns
// the raw MAC bytes. Signing base64-encodes the result; verification compares
// raw bytes via crypto/hmac.Equal.
func computeMAC(secret []byte, deliveryID DeliveryID, timestamp string, payload []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(deliveryID))
	mac.Write([]byte(signedContentSep))
	mac.Write([]byte(timestamp))
	mac.Write([]byte(signedContentSep))
	mac.Write(payload)
	return mac.Sum(nil)
}
