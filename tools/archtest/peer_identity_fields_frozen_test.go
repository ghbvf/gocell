package archtest

// peer_identity_fields_frozen_test.go locks the curated field set of
// pkg/ctxkeys.PeerIdentity.
//
// INVARIANT: PEER-IDENTITY-FIELDS-FROZEN-01
//
// ctxkeys.PeerIdentity is the curated request-scope view of a verified leaf
// X.509 client certificate (WM-32, ADR docs/architecture/
// 202605290130-049-adr-mtls-server-builder-and-identity-hook.md §Decision-4).
// The ADR's design promise is that the type surfaces exactly
// {Subject pkix.Name, DNSNames []string, URIs []*url.URL} and DELIBERATELY
// never exposes the raw *x509.Certificate, so downstream handlers cannot
// couple to the x509 internal representation.
//
// This reflect lock is what makes that promise genuinely Hard rather than a
// godoc convention: adding any field (most dangerously `Raw *x509.Certificate`),
// removing one, embedding a struct, unexporting a field, or changing a field's
// type all trip an exact-set assertion in CI. Before this archtest, the
// "no raw cert / curated field set" claim was enforced by nothing but a
// comment in peer_identity.go — see ADR §Decision-4 (which this archtest
// upgrades from "godoc 约定" to archtest-enforced) and the F1–F3 review of
// PR #1250.
//
// AI-robust rating (.claude/rules/gocell/ai-robust.md §Hard 范本目录,
// "codegen funnel + golden" sibling = reflect field-freeze; see
// DETAILS-SEALED-FIELD-FROZEN-01 / SUBSCRIBERS-DERIVED-FIELD-FROZEN-01 /
// OUTBOX-HANDLERESULT-FIELDS-FROZEN-01): Hard. A field-set drift is
// inexpressible without a CI-visible failure. NOTE the scope boundary — this
// freezes the FIELD SET (the "no raw cert" surface). It does NOT enforce that
// runtime/http/middleware.MTLS is the SOLE producer of PeerIdentity values;
// that sole-producer property is an unenforced convention (a cross-package
// context.WithValue setter cannot be sealed in Go — caller-allowlist would be
// at most Medium), and is intentionally NOT claimed as Hard in the ADR.
//
// Blind spots of a reflect field-freeze, each covered below or by the reverse
// self-check (TestPeerIdentityFieldsFrozen01_ReverseBlindSpot):
//   - Embedding: an anonymous field would smuggle a whole struct's surface
//     past a name-keyed check → guarded explicitly (f.Anonymous → violation).
//   - Type alias re-shape (`type PeerIdentity = otherStruct`): reflect resolves
//     to the underlying struct, so the exact field-set check still fires.
//   - Wrong field type / unexported field / extra / missing field: covered by
//     the exact name→type map + NumField check.
// The reverse self-check proves the detector is non-vacuous by running it
// against deliberately-malformed local structs.

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"net/url"
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// peerIdentityWantFields is the frozen curated field set: name → reflect type
// string. Changing this set is a wire/contract surface change that must be made
// together with ADR 202605290130-049 Row 6.
var peerIdentityWantFields = map[string]string{
	"Subject":  "pkix.Name",
	"DNSNames": "[]string",
	"URIs":     "[]*url.URL",
}

// checkPeerIdentityShape returns a list of violation messages for dt against
// the frozen PeerIdentity field set; an empty result means dt conforms. It is
// extracted from the test body so the reverse self-check can prove the detector
// flags malformed shapes (non-vacuous Hard guard).
func checkPeerIdentityShape(dt reflect.Type) []string {
	if dt.Kind() != reflect.Struct {
		return []string{fmt.Sprintf("Kind = %s, want struct", dt.Kind())}
	}
	var violations []string
	if dt.NumField() != len(peerIdentityWantFields) {
		violations = append(violations, fmt.Sprintf(
			"NumField = %d, want exactly %d", dt.NumField(), len(peerIdentityWantFields)))
	}
	for i := 0; i < dt.NumField(); i++ {
		f := dt.Field(i)
		if f.Anonymous {
			violations = append(violations, fmt.Sprintf(
				"field %q is embedded (embedding re-opens the cert-internal surface)", f.Name))
			continue
		}
		if !f.IsExported() {
			violations = append(violations, fmt.Sprintf(
				"field %q is unexported (curated fields are the handler read surface)", f.Name))
		}
		ts := f.Type.String()
		if ts == "x509.Certificate" || ts == "*x509.Certificate" {
			violations = append(violations, fmt.Sprintf(
				"field %q exposes raw %s (the curated type never surfaces the raw certificate)", f.Name, ts))
		}
		wantType, ok := peerIdentityWantFields[f.Name]
		if !ok {
			violations = append(violations, fmt.Sprintf("unexpected field %q (%s)", f.Name, ts))
			continue
		}
		if ts != wantType {
			violations = append(violations, fmt.Sprintf(
				"field %q type = %q, want %q", f.Name, ts, wantType))
		}
	}
	return violations
}

func TestPeerIdentityFieldsFrozen01(t *testing.T) {
	t.Parallel()
	violations := checkPeerIdentityShape(reflect.TypeOf(ctxkeys.PeerIdentity{}))
	for _, v := range violations {
		t.Errorf("PEER-IDENTITY-FIELDS-FROZEN-01: %s. The curated set is %v; if this change is "+
			"intentional, update ADR 202605290130-049 Row 6 + §Decision-4 and this golden in the same PR.",
			v, peerIdentityWantFields)
	}
}

// TestPeerIdentityFieldsFrozen01_ReverseBlindSpot proves checkPeerIdentityShape
// is not vacuous: a conforming shape yields zero violations, and each
// deliberately-malformed shape (raw-cert field, embedding, wrong type) yields
// at least one. If this regresses, the freeze above could silently pass on a
// drifted PeerIdentity.
func TestPeerIdentityFieldsFrozen01_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	type good struct {
		Subject  pkix.Name
		DNSNames []string
		URIs     []*url.URL
	}
	if v := checkPeerIdentityShape(reflect.TypeOf(good{})); len(v) != 0 {
		t.Errorf("PEER-IDENTITY-FIELDS-FROZEN-01 self-test: detector flagged a conforming shape "+
			"(vacuous-pass risk): %v", v)
	}

	type withRawCert struct {
		Subject  pkix.Name
		DNSNames []string
		URIs     []*url.URL
		Raw      *x509.Certificate
	}
	type embedded struct {
		pkix.Name
		DNSNames []string
		URIs     []*url.URL
	}
	type wrongType struct {
		Subject  string // was pkix.Name
		DNSNames []string
		URIs     []*url.URL
	}
	bad := map[string]reflect.Type{
		"extra-raw-cert-field": reflect.TypeOf(withRawCert{}),
		"embedded-field":       reflect.TypeOf(embedded{}),
		"wrong-field-type":     reflect.TypeOf(wrongType{}),
	}
	for name, dt := range bad {
		if v := checkPeerIdentityShape(dt); len(v) == 0 {
			t.Errorf("PEER-IDENTITY-FIELDS-FROZEN-01 self-test: detector passed malformed shape %q "+
				"(blind spot): expected at least one violation", name)
		}
	}
}
