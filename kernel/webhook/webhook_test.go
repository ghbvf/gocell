package webhook

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestSignedHeaders_ZeroValueApply_FailClosed is the #1733 F1 reverse self-check:
// a zero-value SignedHeaders (the only form a package-external caller can
// construct — unexported fields incl. `valid`) must write NO signature headers via
// Apply (valid-token fail-close), while a Sign-produced one (valid==true) does.
// This proves the provenance closure is the `valid` flag, not a no-op Apply.
func TestSignedHeaders_ZeroValueApply_FailClosed(t *testing.T) {
	t.Parallel()

	var zero SignedHeaders // valid == false — mirrors external `var h webhook.SignedHeaders`
	zeroHdr := http.Header{}
	zero.Apply(zeroHdr)
	assert.Empty(t, zeroHdr.Get(HeaderID), "zero-value Apply must not write webhook-id")
	assert.Empty(t, zeroHdr.Get(HeaderTimestamp), "zero-value Apply must not write webhook-timestamp")
	assert.Empty(t, zeroHdr.Get(HeaderSignature), "zero-value Apply must not write webhook-signature")

	src, err := NewSource(MustSourceID("s"), minSecret())
	require.NoError(t, err)
	signer, err := NewHMACSigner(src)
	require.NoError(t, err)
	signed, err := signer.Sign([]byte("b"), time.Unix(1700000000, 0), MustDeliveryID("d1"))
	require.NoError(t, err)
	signedHdr := http.Header{}
	signed.Apply(signedHdr)
	assert.NotEmpty(t, signedHdr.Get(HeaderSignature),
		"Sign-produced (valid) Apply must write the signature header — guard is the valid flag, not a blanket no-op")
}

// redactionMask is the literal a redacted secret renders as (pkg/redaction.Mask).
const redactionMask = "<REDACTED>"

// fixedClock returns a clock pinned to a deterministic instant for tests that
// do not depend on a specific vector timestamp.
func fixedClock(t *testing.T) clock.Clock {
	t.Helper()
	return clockmock.New(time.Unix(1700000000, 0))
}

func nowUnixString(clk clock.Clock) string {
	return strconv.FormatInt(clk.Now().Unix(), 10)
}

func minSecret() []byte { return bytes.Repeat([]byte("k"), minSecretLen) }

func TestAlgorithmValidate(t *testing.T) {
	t.Parallel()
	assert.NoError(t, AlgorithmHMACSHA256.Validate())
	assert.Equal(t, "hmac-sha256", AlgorithmHMACSHA256.String())

	err := Algorithm("hmac-sha1").Validate()
	requireErrCode(t, err, errcode.ErrWebhookAlgorithmUnsupported, errcode.KindInvalid)
}

func TestSourceIDValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid", "stripe", false},
		{"valid_with_sep", "shopify-orders_v1", false},
		{"empty", "", true},
		{"uppercase", "Stripe", true},
		{"leading_digit", "1stripe", true},
		{"too_long", strings.Repeat("a", sourceIDMaxLen+1), true},
		{"dot_forbidden", "stripe.payments", true},
		{"max_len_valid", strings.Repeat("a", sourceIDMaxLen), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := NewSourceID(tc.in)
			if tc.wantErr {
				requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
				assert.Equal(t, SourceID(""), id)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, SourceID(tc.in), id)
		})
	}
}

func TestMustSourceID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, SourceID("stripe"), MustSourceID("stripe"))
	assert.Panics(t, func() { MustSourceID("Bad-ID") })
}

func TestNewtypeString(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "stripe", SourceID("stripe").String())
	assert.Equal(t, "evt_1", DeliveryID("evt_1").String())
}

func TestDeliveryIDValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"uuid_like", "msg_p5jXN8AQM9LWM0D4loKWxJek", false},
		{"colon_dot_segments", "evt:1.2-3_x", false},
		{"empty", "", true},
		{"whitespace", "evt 001", true},
		{"too_long", strings.Repeat("a", deliveryIDMaxLen+1), true},
		{"max_len_valid", strings.Repeat("a", deliveryIDMaxLen), false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			id, err := NewDeliveryID(tc.in)
			if tc.wantErr {
				requireErrCode(t, err, errcode.ErrWebhookInvalidHeader, errcode.KindInvalid)
				assert.Equal(t, DeliveryID(""), id)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, DeliveryID(tc.in), id)
		})
	}
}

func TestMustDeliveryID(t *testing.T) {
	t.Parallel()
	assert.Equal(t, DeliveryID("evt_1"), MustDeliveryID("evt_1"))
	assert.Panics(t, func() { MustDeliveryID("evt 1") })
}

func TestNewSource(t *testing.T) {
	t.Parallel()

	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		src, err := NewSource(MustSourceID("stripe"), minSecret())
		require.NoError(t, err)
		assert.Equal(t, SourceID("stripe"), src.ID())
	})

	t.Run("secret_too_short", func(t *testing.T) {
		t.Parallel()
		_, err := NewSource(MustSourceID("stripe"), []byte("short"))
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})

	t.Run("bad_source_id", func(t *testing.T) {
		t.Parallel()
		_, err := NewSource(SourceID("Bad"), minSecret())
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})

	t.Run("defensive_copy", func(t *testing.T) {
		t.Parallel()
		secret := minSecret()
		src, err := NewSource(MustSourceID("stripe"), secret)
		require.NoError(t, err)
		signer, err := NewHMACSigner(src)
		require.NoError(t, err)
		ts := time.Unix(1700000000, 0)
		before, err := signer.Sign([]byte("body"), ts, MustDeliveryID("d1"))
		require.NoError(t, err)

		// Mutating the caller's slice must not change the source's secret.
		for i := range secret {
			secret[i] = 'x'
		}
		after, err := signer.Sign([]byte("body"), ts, MustDeliveryID("d1"))
		require.NoError(t, err)
		// Same-package: compare SignedHeaders' unexported signature field.
		assert.Equal(t, before.signature, after.signature)
	})
}

// TestSourceLogValueRedactsSecret asserts the slog.LogValuer implementation
// never emits the raw secret — the type-system half of the Source-leak defense.
func TestSourceLogValueRedactsSecret(t *testing.T) {
	t.Parallel()
	rawSecret := []byte("topsecret-hmac-key-material")
	src, err := NewSource(MustSourceID("stripe"), rawSecret)
	require.NoError(t, err)

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("verifying", slog.Any("source", src))

	out := buf.String()
	assert.Contains(t, out, redactionMask, "secret must render as the mask")
	assert.NotContains(t, out, string(rawSecret), "raw secret must never reach the log")
	assert.Contains(t, out, "stripe", "non-secret id is still observable")

	// Assert the structured value directly too.
	v := src.LogValue()
	require.Equal(t, slog.KindGroup, v.Kind())
	var secretAttr string
	for _, a := range v.Group() {
		if a.Key == "secret" {
			secretAttr = a.Value.String()
		}
	}
	assert.Equal(t, redactionMask, secretAttr)
}

// TestSourceFmtRedactsSecret asserts the fmt-path defense (F1): String and
// GoString keep the secret out of every fmt/log verb, since slog.LogValuer does
// not cover fmt/log/panic.
func TestSourceFmtRedactsSecret(t *testing.T) {
	t.Parallel()
	rawSecret := []byte("topsecret-hmac-key-material")
	src, err := NewSource(MustSourceID("stripe"), rawSecret)
	require.NoError(t, err)

	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		out := fmt.Sprintf(verb, src)
		assert.NotContains(t, out, string(rawSecret), "verb %s must not leak the raw secret", verb)
		assert.Contains(t, out, redactionMask, "verb %s must render the mask", verb)
		assert.Contains(t, out, "stripe", "verb %s keeps the non-secret id observable", verb)
	}

	// A Source embedded in an error wrapped with %v must also stay redacted.
	wrapped := fmt.Errorf("processing %v: boom", src)
	assert.NotContains(t, wrapped.Error(), string(rawSecret))
}

// TestSentinelKindMappingAtConstructionSites verifies that the webhook
// sentinels reachable in PR-1 are constructed with the intended Kind, so the
// Kind→HTTP-status contract holds at the real construction points (errcode_test
// only freezes the code strings).
func TestSentinelKindMappingAtConstructionSites(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		trigger    func() error
		wantCode   errcode.Code
		wantStatus int
	}{
		{
			name:       "algorithm_unsupported_400",
			trigger:    func() error { return Algorithm("sha1").Validate() },
			wantCode:   errcode.ErrWebhookAlgorithmUnsupported,
			wantStatus: 400,
		},
		{
			name:       "config_invalid_400",
			trigger:    func() error { return SourceID("").Validate() },
			wantCode:   errcode.ErrWebhookConfigInvalid,
			wantStatus: 400,
		},
		{
			name:       "invalid_header_400",
			trigger:    func() error { return DeliveryID("").Validate() },
			wantCode:   errcode.ErrWebhookInvalidHeader,
			wantStatus: 400,
		},
		{
			name: "timestamp_expired_401",
			trigger: func() error {
				v, _ := NewHMACVerifier(fixedClock(t))
				src, _ := NewSource(MustSourceID("s"), minSecret())
				return v.Verify([]byte("b"),
					Headers{DeliveryID: "d1", Timestamp: "1", Signature: "v1,AA=="}, src)
			},
			wantCode:   errcode.ErrWebhookTimestampExpired,
			wantStatus: 401,
		},
		{
			name: "invalid_signature_401",
			trigger: func() error {
				clk := fixedClock(t)
				v, _ := NewHMACVerifier(clk)
				src, _ := NewSource(MustSourceID("s"), minSecret())
				return v.Verify([]byte("b"), Headers{
					DeliveryID: "d1",
					Timestamp:  nowUnixString(clk),
					Signature:  "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
				}, src)
			},
			wantCode:   errcode.ErrWebhookInvalidSignature,
			wantStatus: 401,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.trigger()
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "want *errcode.Error, got %v", err)
			assert.Equal(t, tc.wantCode, ec.Code)
			assert.Equal(t, tc.wantStatus, ec.Kind.Status())
		})
	}
}

// requireErrCode asserts err is an *errcode.Error with the given code and kind.
func requireErrCode(t *testing.T, err error, code errcode.Code, kind errcode.Kind) {
	t.Helper()
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "want *errcode.Error, got %v", err)
	assert.Equal(t, code, ec.Code)
	assert.Equal(t, kind, ec.Kind)
}
