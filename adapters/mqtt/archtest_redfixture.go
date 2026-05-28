//go:build archtest_fixture

// archtest_redfixture.go is loaded only when the archtest_fixture build tag is
// set. The function below constructs a non-zero ClientID outside the
// sanctioned assembleClientID funnel — a DELIBERATE violation of
// MQTT-CLIENT-ID-NAMESPACE-01/A2.
//
// The archtest TestMQTTFunnel_A2ScannerFiresOnRedFixture in
// tools/archtest/mqtt_funnel_test.go loads this package with the
// archtest_fixture tag and asserts the scanner reports ≥1 violation here.
// Without this red fixture, a regression that silently breaks the scanner's
// type-resolution path would still pass the production-only A2 self-check
// (which only verifies the inside-count is ≥1, not that outside violations
// are detected).
//
// Why in-package: ClientID.value is unexported, so an external-package
// fixture cannot construct ClientID{value: ...} — it would be a compile
// error. Only an in-package file can plant the red literal. The
// //go:build archtest_fixture directive isolates this file from normal go
// build / go test runs; only archtest's RunTypedFixture loader includes it.
//
// ref: tools/archtest/mqtt_funnel_test.go TestMQTTFunnel_A2ScannerFiresOnRedFixture
// ref: tools/archtest/fixture.go FixtureOpts (sealed Tags-field-free input)

package mqtt

// archtestRedFixtureClientID intentionally violates MQTT-CLIENT-ID-NAMESPACE-01/A2:
// the non-zero ClientID composite literal is constructed outside
// assembleClientID. The A2 scanner MUST report this as a violation when the
// package is loaded with the archtest_fixture build tag.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading; never executed.
func archtestRedFixtureClientID() ClientID {
	return ClientID{value: "intentional-archtest-violation"}
}
