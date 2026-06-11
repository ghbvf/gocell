//go:build archtest_fixture

// Package mqttreasonfixture is the reverse/anti-vacuity fixture for
// MQTT-CONNACK-REASON-TABLE-COMPLETE-01, MQTT-PUBACK-REASON-TABLE-COMPLETE-01,
// MQTT-SUBACK-REASON-TABLE-COMPLETE-01, and MQTT-REASON-TABLE-POSITIONAL-01.
//
// It declares deliberately defective reason tables:
//   - connackReasonTableFixture is MISSING spec code 0x81 (MalformedPacket),
//     so the completeness scanner must report it as absent.
//   - pubackReasonTableFixture uses named-field literals (key:value syntax),
//     so MQTT-REASON-TABLE-POSITIONAL-01 must flag it.
//
// Loaded via Run(t, Fixture(FixtureOpts{Tests:false},
// []string{"./tools/archtest/internal/mqttreasonfixture/..."})) with the
// archtest_fixture build tag, so it never appears in a normal build or test.
//
// DO NOT use this package in production code.
package mqttreasonfixture

// fixtureConnackClass mirrors the shape of the production connackClass type so
// the scanner can find the var decl and check its CompositeLit rows without
// importing the mqtt package (unexported types are inaccessible cross-package).
type fixtureConnackClass uint8

const (
	fixtureClassTransient     fixtureConnackClass = 1
	fixtureClassBootstrapFatal fixtureConnackClass = 2
)

// fixtureConnackReason mirrors connackReason shape for scanning.
type fixtureConnackReason struct {
	code  byte
	name  string
	class fixtureConnackClass
}

// connackReasonTableFixture deliberately omits spec code 0x81 (MalformedPacket)
// so MQTT-CONNACK-REASON-TABLE-COMPLETE-01 must report it as missing.
// Uses positional literals (correct form) so MQTT-REASON-TABLE-POSITIONAL-01
// does NOT fire on this table.
var connackReasonTableFixture = []fixtureConnackReason{
	{0x00, "Success", fixtureClassTransient},
	{0x80, "UnspecifiedError", fixtureClassTransient},
	// 0x81 MalformedPacket intentionally omitted — the scanner must detect this
	{0x82, "ProtocolError", fixtureClassBootstrapFatal},
}

// fixtureAckReason mirrors ackReason shape for scanning.
type fixtureAckReason struct {
	code    byte
	name    string
	errCode string
	kind    string
}

// pubackReasonTableFixture uses NAMED-FIELD literals, which violates
// MQTT-REASON-TABLE-POSITIONAL-01. The scanner must flag the key:value elements.
//
//nolint:unused // referenced exclusively by the archtest scanner via AST loading.
var pubackReasonTableFixture = []fixtureAckReason{
	{code: 0x00, name: "Success", errCode: "", kind: "KindInternal"},
	{code: 0x10, name: "NoMatchingSubscribers", errCode: "ERR_NO_SUBSCRIBERS", kind: "KindUnavailable"},
}
