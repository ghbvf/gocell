// Package webhooktest provides a sign↔verify conformance suite for
// [webhook.Signer] and [webhook.Verifier] implementations. It is intended for
// _test.go use only; production code must not import this package.
//
// kernel/ layering rule: non-_test.go files in kernel/ must not import testify
// or kernel/clock/clockmock. This package uses stdlib testing.T helpers only
// and accepts a VerifierFactory to avoid importing clockmock directly.
//
// Usage — wire in the package-level conformance call:
//
//	func TestSignerVerifierConformance(t *testing.T) {
//	    webhooktest.RunSignerVerifierConformance(t,
//	        webhook.NewHMACSigner,
//	        func(ts time.Time) (webhook.Verifier, error) {
//	            return webhook.NewHMACVerifier(clockmock.New(ts))
//	        },
//	    )
//	}
package webhooktest

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	conformanceMinSecretLen = 24
)

// VerifierFactory creates a [webhook.Verifier] whose internal clock is pinned
// to ts. The caller provides the factory so conformance.go does not import
// kernel/clock/clockmock (kernel-isolation rule prohibits clockmock in
// non-_test.go kernel files).
type VerifierFactory func(ts time.Time) (webhook.Verifier, error)

// conformanceSecret returns a 24-byte secret adequate for conformance tests.
func conformanceSecret() []byte {
	return bytes.Repeat([]byte("k"), conformanceMinSecretLen)
}

// conformanceSource builds a Source with sourceID "test-source" and the conformance secret.
func conformanceSource(t *testing.T) webhook.Source {
	t.Helper()
	id, err := webhook.NewSourceID("test-source")
	mustNoError(t, err, "NewSourceID")
	src, err := webhook.NewSource(id, conformanceSecret())
	mustNoError(t, err, "NewSource")
	return src
}

// RunSignerVerifierConformance runs the sign↔verify behavioral contract suite.
//
// newSigner is a factory bound to a given Source.
// newVerifier is a factory that pins the verifier's clock to the given time.
//
// Sub-cases:
//  1. round_trip: sign produces Headers that verify passes.
//  2. tamper_body: altering the body after signing → ErrWebhookInvalidSignature.
//  3. tamper_signature: corrupted signature token → ErrWebhookInvalidSignature.
//  4. tamper_timestamp_expired: out-of-window timestamp → ErrWebhookTimestampExpired.
//  5. multi_token_rotation_first_matches: first valid token in multi-token header accepted.
//  6. multi_token_rotation_second_matches: last token in multi-token header also accepted.
//  7. timestamp_outside_window: bidirectional tolerance check.
//  8. empty_secret_source_rejected_by_signer: zero-value Source → ErrWebhookConfigInvalid.
//  9. no_matching_token: two non-matching tokens → ErrWebhookInvalidSignature.
func RunSignerVerifierConformance(
	t *testing.T,
	newSigner func(webhook.Source) (webhook.Signer, error),
	newVerifier VerifierFactory,
) {
	t.Helper()

	base := time.Unix(1_700_000_000, 0)
	basePayload := []byte("conformance-payload")
	src := conformanceSource(t)

	signer, err := newSigner(src)
	mustNoError(t, err, "newSigner(src)")

	deliveryID, err := webhook.NewDeliveryID("conf-del-01")
	mustNoError(t, err, "NewDeliveryID")

	goodHeaders, err := signer.Sign(basePayload, base, deliveryID)
	mustNoError(t, err, "signer.Sign")

	// verifierAt builds a Verifier pinned to ts.
	verifierAt := func(t *testing.T, ts time.Time) webhook.Verifier {
		t.Helper()
		v, verr := newVerifier(ts)
		mustNoError(t, verr, "newVerifier")
		return v
	}

	runRoundTrip(t, src, basePayload, goodHeaders, base, verifierAt)
	runTamperBody(t, src, basePayload, goodHeaders, base, verifierAt)
	runTamperSignature(t, src, basePayload, goodHeaders, base, verifierAt)
	runTamperTimestampExpired(t, src, basePayload, goodHeaders, base, verifierAt)
	runMultiTokenFirstMatches(t, src, basePayload, goodHeaders, base, deliveryID, newSigner, verifierAt)
	runMultiTokenSecondMatches(t, src, basePayload, goodHeaders, base, deliveryID, newSigner, verifierAt)
	runTimestampOutsideWindow(t, src, basePayload, goodHeaders, base, verifierAt)
	runEmptySecretSourceRejected(t, newSigner)
	runNoMatchingToken(t, src, basePayload, goodHeaders, base, verifierAt)
}

func runRoundTrip(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("round_trip", func(t *testing.T) {
		t.Parallel()
		v := verifierAt(t, base)
		requireNoError(t, v.Verify(payload, headers, src),
			"sign→verify must succeed with matching body/headers/source")
	})
}

func runTamperBody(
	t *testing.T, src webhook.Source, _ []byte,
	headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("tamper_body", func(t *testing.T) {
		t.Parallel()
		v := verifierAt(t, base)
		err := v.Verify([]byte("tampered-body"), headers, src)
		requireErrCode(t, err, errcode.ErrWebhookInvalidSignature, errcode.KindUnauthenticated)
	})
}

func runTamperSignature(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("tamper_signature", func(t *testing.T) {
		t.Parallel()
		v := verifierAt(t, base)
		tampered := headers
		tampered.Signature = "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		err := v.Verify(payload, tampered, src)
		requireErrCode(t, err, errcode.ErrWebhookInvalidSignature, errcode.KindUnauthenticated)
	})
}

func runTamperTimestampExpired(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("tamper_timestamp_expired", func(t *testing.T) {
		t.Parallel()
		// Clock is 10 minutes past signing time; default tolerance is 5 min.
		v := verifierAt(t, base.Add(10*time.Minute))
		err := v.Verify(payload, headers, src)
		requireErrCode(t, err, errcode.ErrWebhookTimestampExpired, errcode.KindUnauthenticated)
	})
}

func runMultiTokenFirstMatches(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	deliveryID webhook.DeliveryID,
	newSigner func(webhook.Source) (webhook.Signer, error),
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("multi_token_rotation_first_matches", func(t *testing.T) {
		t.Parallel()
		altHeaders := buildAltHeaders(t, payload, base, deliveryID, "test-source-2", newSigner)
		combined := headers
		combined.Signature = headers.Signature + " " + altHeaders.Signature
		v := verifierAt(t, base)
		requireNoError(t, v.Verify(payload, combined, src),
			"first matching token should satisfy verification")
	})
}

func runMultiTokenSecondMatches(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	deliveryID webhook.DeliveryID,
	newSigner func(webhook.Source) (webhook.Signer, error),
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("multi_token_rotation_second_matches", func(t *testing.T) {
		t.Parallel()
		altHeaders := buildAltHeaders(t, payload, base, deliveryID, "test-source-b", newSigner)
		// Put the "wrong" token first, then the correct one.
		combined := headers
		combined.Signature = altHeaders.Signature + " " + headers.Signature
		v := verifierAt(t, base)
		requireNoError(t, v.Verify(payload, combined, src),
			"any matching token anywhere in the header should satisfy verification")
	})
}

func runTimestampOutsideWindow(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("timestamp_outside_window", func(t *testing.T) {
		t.Parallel()
		// Bidirectional: past the window in the negative direction.
		v := verifierAt(t, base.Add(-10*time.Minute))
		err := v.Verify(payload, headers, src)
		requireErrCode(t, err, errcode.ErrWebhookTimestampExpired, errcode.KindUnauthenticated)
	})
}

func runEmptySecretSourceRejected(
	t *testing.T,
	newSigner func(webhook.Source) (webhook.Signer, error),
) {
	t.Helper()
	t.Run("empty_secret_source_rejected_by_signer", func(t *testing.T) {
		t.Parallel()
		_, err := newSigner(webhook.Source{})
		if err == nil {
			t.Fatal("expected error from newSigner with empty-secret source, got nil")
		}
		requireErrCode(t, err, errcode.ErrWebhookConfigInvalid, errcode.KindInvalid)
	})
}

func runNoMatchingToken(
	t *testing.T, src webhook.Source, payload []byte,
	headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("no_matching_token", func(t *testing.T) {
		t.Parallel()
		v := verifierAt(t, base)
		bad := headers
		bad.Signature = strings.Join([]string{
			"v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=",
			"v1,BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=",
		}, " ")
		err := v.Verify(payload, bad, src)
		requireErrCode(t, err, errcode.ErrWebhookInvalidSignature, errcode.KindUnauthenticated)
	})
}

// buildAltHeaders creates headers signed by an alternate source (different secret).
func buildAltHeaders(
	t *testing.T, payload []byte, ts time.Time,
	deliveryID webhook.DeliveryID, sourceIDStr string,
	newSigner func(webhook.Source) (webhook.Signer, error),
) webhook.Headers {
	t.Helper()
	id, err := webhook.NewSourceID(sourceIDStr)
	mustNoError(t, err, "NewSourceID alt")
	altSecret := bytes.Repeat([]byte("j"), conformanceMinSecretLen)
	src2, err := webhook.NewSource(id, altSecret)
	mustNoError(t, err, "NewSource alt")
	signer2, err := newSigner(src2)
	mustNoError(t, err, "newSigner alt")
	h, err := signer2.Sign(payload, ts, deliveryID)
	mustNoError(t, err, "Sign alt")
	return h
}

// ---------------------------------------------------------------------------
// Internal assertion helpers — stdlib only, no testify in kernel/ non-test files
// (mirrors kernel/outbox/outboxtest/helpers.go pattern)
// ---------------------------------------------------------------------------

// mustNoError fails the test immediately if err is non-nil (setup-time guard).
func mustNoError(t *testing.T, err error, context string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", context, err)
	}
}

// requireNoError fails the test immediately if err is non-nil.
func requireNoError(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", msg, err)
	}
}

// requireErrCode asserts err is an *errcode.Error with the given Code and Kind.
func requireErrCode(t *testing.T, err error, code errcode.Code, kind errcode.Kind) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected *errcode.Error with code=%s kind=%d, got nil", code, kind)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("want *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != code {
		t.Errorf("errcode.Code: got %s, want %s", ec.Code, code)
	}
	if ec.Kind != kind {
		t.Errorf("errcode.Kind: got %d, want %d", ec.Kind, kind)
	}
}
