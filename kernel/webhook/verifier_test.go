package webhook

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Test-time durations extracted to package-level consts (TEST-TIME-LITERAL-01).
const (
	tenMinutes        = 10 * time.Minute
	oneHourTolerance  = time.Hour
	fiveMinutes       = 5 * time.Minute
	justOverTolerance = 5*time.Minute + time.Second
)

// TestVerify_Vectors drives every golden vector through the verifier with the
// clock pinned to the vector's own timestamp (so the window check passes for
// valid vectors and only the signature determines the outcome).
func TestVerify_Vectors(t *testing.T) {
	t.Parallel()
	for _, v := range loadVectors(t) {
		v := v
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			src, err := NewSource(MustSourceID(v.SourceID), v.secretBytes(t))
			require.NoError(t, err)
			verifier, err := NewHMACVerifier(clockmockAt(v.unixTime(t)))
			require.NoError(t, err)

			err = verifier.Verify([]byte(v.Payload), Headers{
				DeliveryID: MustDeliveryID(v.DeliveryID),
				Timestamp:  v.Timestamp,
				Signature:  v.Signature,
			}, src)

			if v.Valid {
				assert.NoError(t, err)
				return
			}
			requireErrCode(t, err, errcode.ErrWebhookInvalidSignature, errcode.KindUnauthenticated)
		})
	}
}

func TestNewHMACVerifier_Options(t *testing.T) {
	t.Parallel()

	t.Run("nil_clock_panics", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() { _, _ = NewHMACVerifier(nil) })
	})

	t.Run("nil_option_ignored", func(t *testing.T) {
		t.Parallel()
		_, err := NewHMACVerifier(fixedClock(t), nil)
		require.NoError(t, err)
	})

	t.Run("non_positive_tolerance_rejected", func(t *testing.T) {
		t.Parallel()
		_, err := NewHMACVerifier(fixedClock(t), WithTolerance(0))
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})

	t.Run("custom_tolerance_applies", func(t *testing.T) {
		t.Parallel()
		// Sign at T, verify at T+10min with a 1h tolerance → accepted.
		base := time.Unix(1700000000, 0)
		src, err := NewSource(MustSourceID("s"), minSecret())
		require.NoError(t, err)
		signer, err := NewHMACSigner(src)
		require.NoError(t, err)
		headers, err := signer.Sign([]byte("body"), base, MustDeliveryID("d1"))
		require.NoError(t, err)

		verifier, err := NewHMACVerifier(clockmockAt(base.Add(tenMinutes)), WithTolerance(oneHourTolerance))
		require.NoError(t, err)
		assert.NoError(t, verifier.Verify([]byte("body"), headers, src))
	})
}

func TestVerify_Errors(t *testing.T) {
	t.Parallel()
	base := time.Unix(1700000000, 0)
	src, err := NewSource(MustSourceID("s"), minSecret())
	require.NoError(t, err)
	signer, err := NewHMACSigner(src)
	require.NoError(t, err)
	good, err := signer.Sign([]byte("body"), base, MustDeliveryID("d1"))
	require.NoError(t, err)

	cases := []struct {
		name     string
		headers  Headers
		body     []byte
		source   Source
		clockAt  time.Time
		wantCode errcode.Code
		wantKind errcode.Kind
	}{
		{
			name:     "empty_secret_source",
			headers:  good,
			body:     []byte("body"),
			source:   Source{},
			clockAt:  base,
			wantCode: errcode.ErrWebhookConfigInvalid,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "bad_delivery_id",
			headers:  Headers{DeliveryID: DeliveryID("bad id"), Timestamp: good.Timestamp, Signature: good.Signature},
			body:     []byte("body"),
			source:   src,
			clockAt:  base,
			wantCode: errcode.ErrWebhookInvalidHeader,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "non_numeric_timestamp",
			headers:  Headers{DeliveryID: good.DeliveryID, Timestamp: "not-a-number", Signature: good.Signature},
			body:     []byte("body"),
			source:   src,
			clockAt:  base,
			wantCode: errcode.ErrWebhookInvalidHeader,
			wantKind: errcode.KindInvalid,
		},
		{
			name:     "timestamp_outside_window",
			headers:  good,
			body:     []byte("body"),
			source:   src,
			clockAt:  base.Add(tenMinutes), // > default 5min
			wantCode: errcode.ErrWebhookTimestampExpired,
			wantKind: errcode.KindUnauthenticated,
		},
		{
			name:     "timestamp_outside_window_past",
			headers:  good,
			body:     []byte("body"),
			source:   src,
			clockAt:  base.Add(-tenMinutes), // bidirectional
			wantCode: errcode.ErrWebhookTimestampExpired,
			wantKind: errcode.KindUnauthenticated,
		},
		{
			name:     "tampered_body",
			headers:  good,
			body:     []byte("tampered"),
			source:   src,
			clockAt:  base,
			wantCode: errcode.ErrWebhookInvalidSignature,
			wantKind: errcode.KindUnauthenticated,
		},
		{
			name:     "no_valid_signature_token",
			headers:  Headers{DeliveryID: good.DeliveryID, Timestamp: good.Timestamp, Signature: "v2,abc malformed"},
			body:     []byte("body"),
			source:   src,
			clockAt:  base,
			wantCode: errcode.ErrWebhookInvalidSignature,
			wantKind: errcode.KindUnauthenticated,
		},
		{
			name:     "malformed_base64_token",
			headers:  Headers{DeliveryID: good.DeliveryID, Timestamp: good.Timestamp, Signature: "v1,@@@not-base64@@@"},
			body:     []byte("body"),
			source:   src,
			clockAt:  base,
			wantCode: errcode.ErrWebhookInvalidSignature,
			wantKind: errcode.KindUnauthenticated,
		},
		{
			name:     "empty_signature",
			headers:  Headers{DeliveryID: good.DeliveryID, Timestamp: good.Timestamp, Signature: ""},
			body:     []byte("body"),
			source:   src,
			clockAt:  base,
			wantCode: errcode.ErrWebhookInvalidSignature,
			wantKind: errcode.KindUnauthenticated,
		},
		{
			name:     "timestamp_one_second_past_tolerance",
			headers:  good,
			body:     []byte("body"),
			source:   src,
			clockAt:  base.Add(justOverTolerance),
			wantCode: errcode.ErrWebhookTimestampExpired,
			wantKind: errcode.KindUnauthenticated,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			verifier, verr := NewHMACVerifier(clockmockAt(tc.clockAt))
			require.NoError(t, verr)
			err := verifier.Verify(tc.body, tc.headers, tc.source)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "want *errcode.Error, got %v", err)
			assert.Equal(t, tc.wantCode, ec.Code)
			assert.Equal(t, tc.wantKind, ec.Kind)
		})
	}
}

// TestVerify_AcceptsAtExactTolerance checks that skew == tolerance is accepted
// (the check is strict `>`, so skew equal to tolerance must pass).
func TestVerify_AcceptsAtExactTolerance(t *testing.T) {
	t.Parallel()
	base := time.Unix(1700000000, 0)
	src, err := NewSource(MustSourceID("s"), minSecret())
	require.NoError(t, err)
	signer, err := NewHMACSigner(src)
	require.NoError(t, err)
	good, err := signer.Sign([]byte("body"), base, MustDeliveryID("d1"))
	require.NoError(t, err)

	// Clock is exactly fiveMinutes after signing — skew == tolerance, must pass.
	verifier, err := NewHMACVerifier(clockmockAt(base.Add(fiveMinutes)))
	require.NoError(t, err)
	assert.NoError(t, verifier.Verify([]byte("body"), good, src))
}
