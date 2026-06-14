package webhook

import (
	"encoding/base64"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// Compile-time assertion: the only Signer/Verifier implementations are the
// in-package HMAC types. The unexported sealed() marker makes any
// package-external implementation a compile error (WEBHOOK-HMAC-FUNNEL-01/A3).
var (
	_ Signer   = (*hmacSigner)(nil)
	_ Verifier = (*hmacVerifier)(nil)
)

func TestNewHMACSigner_RejectsEmptySecret(t *testing.T) {
	t.Parallel()
	// A zero-value Source (constructible from outside the package) has no secret.
	_, err := NewHMACSigner(Source{})
	requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
}

func TestSigner_RejectsBadDeliveryID(t *testing.T) {
	t.Parallel()
	src, err := NewSource(MustSourceID("s"), minSecret())
	require.NoError(t, err)
	signer, err := NewHMACSigner(src)
	require.NoError(t, err)
	_, err = signer.Sign([]byte("b"), time.Unix(1700000000, 0), DeliveryID("bad id"))
	requireErrCode(t, err, errcode.ErrWebhookInvalidHeader, errcode.KindInvalid)
}

// TestSign_MatchesSvixGoldenVector signs the canonical Svix vector and asserts
// byte-for-byte agreement with the reference implementation.
func TestSign_MatchesSvixGoldenVector(t *testing.T) {
	t.Parallel()
	v := loadVector(t, "svix_official_basic")
	src, err := NewSource(MustSourceID(v.SourceID), v.secretBytes(t))
	require.NoError(t, err)
	signer, err := NewHMACSigner(src)
	require.NoError(t, err)

	tsInt, err := strconv.ParseInt(v.Timestamp, 10, 64)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte(v.Payload), time.Unix(tsInt, 0), MustDeliveryID(v.DeliveryID))
	require.NoError(t, err)

	// Same-package test: read SignedHeaders' unexported fields directly.
	assert.Equal(t, v.Signature, signed.signature)
	assert.Equal(t, v.Timestamp, signed.timestamp)
	assert.Equal(t, DeliveryID(v.DeliveryID), signed.deliveryID)
}

// TestSign_SignerVerifierRoundTrip signs then verifies with the same source.
func TestSign_SignerVerifierRoundTrip(t *testing.T) {
	t.Parallel()
	src, err := NewSource(MustSourceID("roundtrip"), minSecret())
	require.NoError(t, err)
	signer, err := NewHMACSigner(src)
	require.NoError(t, err)

	ts := time.Unix(1700000000, 0)
	body := []byte(`{"hello":"world"}`)
	signed, err := signer.Sign(body, ts, MustDeliveryID("evt_rt"))
	require.NoError(t, err)

	// Signature must be a well-formed v1 token decoding to 32 MAC bytes.
	require.True(t, len(signed.signature) > 3 && signed.signature[:3] == "v1,")
	raw, err := base64.StdEncoding.DecodeString(signed.signature[3:])
	require.NoError(t, err)
	assert.Len(t, raw, 32)

	verifier, err := NewHMACVerifier(clockmockAt(ts))
	require.NoError(t, err)
	// Bridge sealed outbound SignedHeaders → inbound Headers (same-package: read
	// unexported fields) to feed Verify, mirroring the receiver's wire reconstruction.
	inbound := Headers{DeliveryID: signed.deliveryID, Timestamp: signed.timestamp, Signature: signed.signature}
	assert.NoError(t, verifier.Verify(body, inbound, src))
}
