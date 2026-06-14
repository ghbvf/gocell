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
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/webhook"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// inboundFromSigned converts a sealed outbound [webhook.SignedHeaders] (the Sign
// output, #1492) into the inbound [webhook.Headers] DTO that
// [webhook.Verifier.Verify] consumes, using the public read-only accessors (the
// seal is on construction, not reading). This is a unit-level bridge that does
// NOT go through the HTTP wire path; the dedicated apply_wire_roundtrip
// conformance case ([runApplyWireRoundTrip]) exercises [webhook.SignedHeaders.Apply]
// → http.Header → parse end-to-end.
func inboundFromSigned(s webhook.SignedHeaders) webhook.Headers {
	return webhook.Headers{
		DeliveryID: s.DeliveryID(),
		Timestamp:  s.Timestamp(),
		Signature:  s.Signature(),
	}
}

const (
	conformanceMinSecretLen = 24

	// conformanceOutOfWindowSkew is the timestamp offset used to put a
	// delivery outside the default ±5min tolerance window in the bidirectional
	// timestamp-window test cases.
	conformanceOutOfWindowSkew = 10 * time.Minute
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

	signed, err := signer.Sign(basePayload, base, deliveryID)
	mustNoError(t, err, "signer.Sign")
	// goodHeaders is the inbound DTO derived from the signed output via the
	// read-only accessors (see inboundFromSigned). The dedicated apply_wire_roundtrip
	// case below is the one that exercises the real Apply → http.Header → parse path.
	goodHeaders := inboundFromSigned(signed)

	// verifierAt builds a Verifier pinned to ts.
	verifierAt := func(t *testing.T, ts time.Time) webhook.Verifier {
		t.Helper()
		v, verr := newVerifier(ts)
		mustNoError(t, verr, "newVerifier")
		return v
	}

	runRoundTrip(t, src, basePayload, goodHeaders, base, verifierAt)
	runApplyWireRoundTrip(t, src, basePayload, signed, base, verifierAt)
	runTamperBody(t, src, basePayload, goodHeaders, base, verifierAt)
	runTamperSignature(t, src, basePayload, goodHeaders, base, verifierAt)
	runTamperTimestampExpired(t, src, basePayload, goodHeaders, base, verifierAt)
	multiToken := multiTokenFixture{
		Source:     src,
		Payload:    basePayload,
		Headers:    goodHeaders,
		Base:       base,
		DeliveryID: deliveryID,
		NewSigner:  newSigner,
		VerifierAt: verifierAt,
	}
	runMultiTokenFirstMatches(t, multiToken)
	runMultiTokenSecondMatches(t, multiToken)
	runTimestampOutsideWindow(t, src, basePayload, goodHeaders, base, verifierAt)
	runEmptySecretSourceRejected(t, newSigner)
	runEmptySecretSourceRejectedByVerifier(t, basePayload, goodHeaders, base, verifierAt)
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

// runApplyWireRoundTrip is the ONLY conformance case that exercises the real
// outbound write path [webhook.SignedHeaders.Apply] → http.Header → read-back by
// the three header-name constants → verify. The other cases bridge via accessors
// ([inboundFromSigned]), so this case is what catches an Apply field-mapping swap
// (e.g. writing the signature under HeaderID) or an Apply ↔ accessor divergence.
func runApplyWireRoundTrip(
	t *testing.T, src webhook.Source, payload []byte,
	signed webhook.SignedHeaders, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("apply_wire_roundtrip", func(t *testing.T) {
		t.Parallel()
		h := http.Header{}
		signed.Apply(h)
		// Apply must map each field to its sanctioned header name and agree with
		// the read-only accessors.
		requireEqual(t, string(signed.DeliveryID()), h.Get(webhook.HeaderID), "Apply HeaderID")
		requireEqual(t, signed.Timestamp(), h.Get(webhook.HeaderTimestamp), "Apply HeaderTimestamp")
		requireEqual(t, signed.Signature(), h.Get(webhook.HeaderSignature), "Apply HeaderSignature")
		// Reconstruct the inbound Headers off the wire and verify end-to-end.
		inbound := webhook.Headers{
			DeliveryID: webhook.DeliveryID(h.Get(webhook.HeaderID)),
			Timestamp:  h.Get(webhook.HeaderTimestamp),
			Signature:  h.Get(webhook.HeaderSignature),
		}
		v := verifierAt(t, base)
		requireNoError(t, v.Verify(payload, inbound, src),
			"sign→Apply→wire→verify round-trip must succeed")
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
		// Clock is conformanceOutOfWindowSkew past signing time; default tolerance is 5 min.
		v := verifierAt(t, base.Add(conformanceOutOfWindowSkew))
		err := v.Verify(payload, headers, src)
		requireErrCode(t, err, errcode.ErrWebhookTimestampExpired, errcode.KindUnauthenticated)
	})
}

type multiTokenFixture struct {
	Source     webhook.Source
	Payload    []byte
	Headers    webhook.Headers
	Base       time.Time
	DeliveryID webhook.DeliveryID
	NewSigner  func(webhook.Source) (webhook.Signer, error)
	VerifierAt func(*testing.T, time.Time) webhook.Verifier
}

func runMultiTokenFirstMatches(t *testing.T, f multiTokenFixture) {
	t.Helper()
	t.Run("multi_token_rotation_first_matches", func(t *testing.T) {
		t.Parallel()
		altHeaders := buildAltHeaders(t, f.Payload, f.Base, f.DeliveryID, "test-source-2", f.NewSigner)
		combined := f.Headers
		combined.Signature = f.Headers.Signature + " " + altHeaders.Signature
		v := f.VerifierAt(t, f.Base)
		requireNoError(t, v.Verify(f.Payload, combined, f.Source),
			"first matching token should satisfy verification")
	})
}

func runMultiTokenSecondMatches(t *testing.T, f multiTokenFixture) {
	t.Helper()
	t.Run("multi_token_rotation_second_matches", func(t *testing.T) {
		t.Parallel()
		altHeaders := buildAltHeaders(t, f.Payload, f.Base, f.DeliveryID, "test-source-b", f.NewSigner)
		// Put the "wrong" token first, then the correct one.
		combined := f.Headers
		combined.Signature = altHeaders.Signature + " " + f.Headers.Signature
		v := f.VerifierAt(t, f.Base)
		requireNoError(t, v.Verify(f.Payload, combined, f.Source),
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
		v := verifierAt(t, base.Add(-conformanceOutOfWindowSkew))
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

// runEmptySecretSourceRejectedByVerifier is the verifier-side sibling of
// runEmptySecretSourceRejected: Verify must independently reject a zero-value
// (empty-secret) Source, not rely on the signer guard. Without this case, a
// dropped verifier-side empty-secret guard would pass conformance.
func runEmptySecretSourceRejectedByVerifier(
	t *testing.T, payload []byte, headers webhook.Headers, base time.Time,
	verifierAt func(*testing.T, time.Time) webhook.Verifier,
) {
	t.Helper()
	t.Run("empty_secret_source_rejected_by_verifier", func(t *testing.T) {
		t.Parallel()
		v := verifierAt(t, base)
		err := v.Verify(payload, headers, webhook.Source{})
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
	signed, err := signer2.Sign(payload, ts, deliveryID)
	mustNoError(t, err, "Sign alt")
	return inboundFromSigned(signed)
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

// requireEqual fails the test immediately if got != want (string equality).
func requireEqual(t *testing.T, want, got, context string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: got %q, want %q", context, got, want)
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
