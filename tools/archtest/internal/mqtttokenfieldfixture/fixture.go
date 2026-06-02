//go:build archtest_fixture

// Package mqtttokenfieldfixture is the typed fixture for
// MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2b + MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3
// field-assignment non-vacuity (F2 requirement per PR #1529 review).
//
// The production token field (`publishableTopic.topic`, `subscribableFilter.wireFilter`)
// is unexported in adapters/mqtt/internal/topicns, so cross-package field
// assignment is a compile error — the real scanner can only exercise against
// code in that one package (where Mint is the sole allowed site). To prove the
// TYPED scanner (mqttIsTokenTyped + mqttEnclosingKeyAllowed) actually fires on
// a genuine outside-allowed-func violation, this package provides:
//
//  1. A REPLICA of PublishableTopic's shape: `type PublishableTopic struct{ topic string }`.
//  2. A sanctioned constructor that assigns `t.topic` inside its body (green site).
//  3. A separate function that assigns `t.topic` OUTSIDE the allowed constructor (red site).
//
// The test TestMQTTPublishCallsiteFunnel_A2b_ScannerNonVacuous_Typed loads this
// fixture via Run(t, Fixture(...)), runs the real parameterized
// scanMQTTTokenFieldAssignment with targetPkgPath=mqtttokenfieldfixture and
// allowedFuncFullName = this package's ParseFixtureTopic FullName, and asserts:
//   - ≥1 diagnostic (the red site is reported)
//   - 0 diagnostics for the inside-constructor assignment (green site not reported)
//
// This proves mqttIsTokenTyped + the typed enclosing-func gate fire on real source.
//
// ref: tools/archtest/mqtt_callsite_funnel_test.go TestMQTTPublishCallsiteFunnel_A2b_ScannerNonVacuous_Typed
// ref: tools/archtest/internal/mqttredfixture/fixture.go (same pattern for CompositeLit A2)
package mqtttokenfieldfixture

// PublishableTopic is a shape replica of topicns.PublishableTopic. The real
// field is unexported in its package (adapters/mqtt/internal/topicns) so
// cross-package assignment is a compile error. This replica lives here to
// allow the typed scanner to exercise the field-assignment detection path.
type PublishableTopic struct {
	topic string //nolint:unused // referenced exclusively by archtest scanner via AST loading.
}

// ParseFixtureTopic is the sole SANCTIONED construction site for
// PublishableTopic in this fixture. The typed non-vacuity test passes
// ParseFixtureTopic's FullName as the allowedFuncFullName so the green
// (inside-allowed) assignment is NOT reported as a violation.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func ParseFixtureTopic(s string) PublishableTopic {
	t := PublishableTopic{}
	t.topic = s // GREEN: inside the allowed constructor — must NOT be reported.
	return t
}

// archtestRedFixtureOutsideAssign intentionally assigns `t.topic` OUTSIDE the
// allowed constructor. The typed non-vacuity test must detect this as a
// violation (≥1 diagnostic).
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func archtestRedFixtureOutsideAssign(s string) PublishableTopic {
	t := PublishableTopic{}
	t.topic = s // RED: outside the allowed constructor — must be reported.
	return t
}
