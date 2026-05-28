package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
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

// Signer produces signature [Headers] for an outbound webhook delivery. The
// interface is sealed (unexported sealed() marker): the sole implementation is
// the HMAC-SHA256 signer from [NewHMACSigner], so package-external types cannot
// satisfy Signer (WEBHOOK-HMAC-FUNNEL-01/A3 upstream).
type Signer interface {
	// Sign computes the signature headers for payload at time ts under
	// deliveryID. ts is supplied by the caller (the dispatcher passes its
	// clock's Now) so the signer holds no clock.
	Sign(payload []byte, ts time.Time, deliveryID DeliveryID) (Headers, error)
	sealed()
}

type hmacSigner struct {
	source Source
}

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

func (s *hmacSigner) Sign(payload []byte, ts time.Time, deliveryID DeliveryID) (Headers, error) {
	if err := deliveryID.Validate(); err != nil {
		return Headers{}, err
	}
	timestamp := strconv.FormatInt(ts.Unix(), 10)
	mac := computeMAC(s.source.secret, deliveryID, timestamp, payload)
	signature := signatureSchemeV1 + signatureSchemeSep + base64.StdEncoding.EncodeToString(mac)
	return Headers{
		DeliveryID: deliveryID,
		Timestamp:  timestamp,
		Signature:  signature,
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
