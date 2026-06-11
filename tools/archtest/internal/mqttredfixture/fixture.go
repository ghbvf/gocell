//go:build archtest_fixture

// Package mqttredfixture is the red fixture for MQTT-CLIENT-ID-NAMESPACE-01/A2
// scanner-fires self-check. It declares a REPLICA of adapters/mqtt.ClientID
// (same single-unexported-string-field sealed shape) and plants an outside-
// allowedFuncs composite-literal violation that the parameterized scanner
// MUST detect.
//
// Why a replica and not the real type: adapters/mqtt.ClientID.value is
// unexported, so this external package cannot construct mqtt.ClientID{...}
// directly (compile error). The shape replica lets us prove the
// generalized scanner's AST + types-resolution path works on any sealed
// struct following the {value string} convention, which is the structural
// invariant the production scanner relies on.
//
// Why not in-package (adapters/mqtt itself): the path-scoped allowlist guard
// `tools/archtest/internal/typeseval/buildtags_test.go::classifySkipTag`
// (added by #944) rejects the archtest_fixture tag in production paths,
// because a production file silently skipped by the default-tag scan is a
// fail-open vector. The internal/ convention here is the sanctioned subtree.
//
// ref: tools/archtest/mqtt_funnel_test.go TestMQTTFunnel_A2ScannerFiresOnRedFixture
// ref: tools/archtest/fixture.go FixtureOpts (sealed Tags-field-free input)
// ref: gh #944 — archtest_fixture path-scoped allowlist
package mqttredfixture

// FixtureClientID is a sealed-struct replica of adapters/mqtt.ClientID. It
// carries exactly one unexported string field, mirroring the real ClientID's
// shape so the generalized A2 scanner exercises the same go/types
// composite-literal-type resolution path.
type FixtureClientID struct {
	value string
}

// ParseFixtureClientID is the sole sanctioned construction site for
// FixtureClientID; the A2 scanner-fires self-check passes "ParseFixtureClientID"
// as the allowedFuncs set when loading this fixture and confirms the inside
// literal is NOT reported as a violation.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func ParseFixtureClientID(s string) FixtureClientID {
	return FixtureClientID{value: s}
}

// archtestRedFixtureOutside intentionally violates the construction funnel:
// the non-zero composite literal is OUTSIDE ParseFixtureClientID. The A2
// scanner must report it as a violation when loaded with the archtest_fixture
// build tag.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func archtestRedFixtureOutside() FixtureClientID {
	return FixtureClientID{value: "intentional-archtest-violation"}
}

// archtestRedFixtureFieldWrite plants a field-write construction bypass: it
// builds a non-zero FixtureClientID via direct field assignment OUTSIDE the
// sanctioned ParseFixtureClientID. The A2b field-write scanner
// (scanSealedFieldWriteConstruction) must report it. `var c FixtureClientID` is
// a zero-value declaration (no composite literal), so this case is invisible to
// the A2 composite-literal scanner — which is exactly the blind spot A2b closes.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func archtestRedFixtureFieldWrite() FixtureClientID {
	var c FixtureClientID
	c.value = "intentional-archtest-fieldwrite-violation"
	return c
}
