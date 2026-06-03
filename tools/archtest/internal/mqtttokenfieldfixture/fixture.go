//go:build archtest_fixture

// Package mqtttokenfieldfixture is the typed fixture for
// MQTT-PUBLISH-CALLSITE-FUNNEL-01/A2b + MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3
// field-assignment non-vacuity (F2 requirement per PR #1529 review).
//
// The production token fields (`publishableTopic.topic`,
// `subscribableFilter.wireFilter`) are unexported in adapters/mqtt/internal/topicns,
// so cross-package field assignment is a compile error — the real scanner can only
// exercise against code in that one package (where Mint is the sole allowed site).
// To prove the TYPED scanner (mqttIsTokenTyped + mqttEnclosingKeyAllowed) actually
// fires on a genuine outside-allowed-func violation, this package provides a
// shape-replica of each token type, each with two assignment sites:
//
//  1. PublishableTopic (field `topic`): ParseFixtureTopic (green, inside-allowed)
//     + archtestRedFixtureOutsideAssign (red, outside-allowed).
//  2. SubscribableFilter (field `wireFilter`): MintFixtureFilter (green)
//     + archtestRedFixtureOutsideFilterAssign (red).
//
// The tests TestMQTTPublishCallsiteFunnel_A2b_ScannerNonVacuous_Typed (publish) and
// TestMQTTSubscribeCallsiteFunnel_S3_FieldAssignScannerNonVacuous (subscribe) load
// this fixture via Run(t, Fixture(...)), run the real typed scanner primitives with
// targetPkgPath=mqtttokenfieldfixture and allowedFuncFullName = the respective
// constructor's FullName, and assert:
//   - ≥1 diagnostic (the red site is reported)
//   - ≥1 inside-constructor assignment seen but NOT reported (green site)
//
// This proves mqttIsTokenTyped + the typed enclosing-func gate fire on real source
// for both the publish and subscribe token types.
//
// ref: tools/archtest/mqtt_callsite_funnel_test.go mqttAssertTypedFieldAssignScannerFires
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

// SubscribableFilter is a shape replica of topicns.SubscribableFilter, the
// subscribe-side counterpart of PublishableTopic. Its real field (wireFilter) is
// likewise unexported in adapters/mqtt/internal/topicns, so cross-package
// assignment is a compile error; this replica lets the typed scanner exercise the
// field-assignment detection path for MQTT-SUBSCRIBE-CALLSITE-FUNNEL-01/S3.
type SubscribableFilter struct {
	wireFilter string //nolint:unused // referenced exclusively by archtest scanner via AST loading.
}

// MintFixtureFilter is the sole SANCTIONED construction site for
// SubscribableFilter in this fixture. The S3 typed non-vacuity test passes
// MintFixtureFilter's FullName as the allowedFuncFullName so the green
// (inside-allowed) assignment is NOT reported as a violation.
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func MintFixtureFilter(s string) SubscribableFilter {
	f := SubscribableFilter{}
	f.wireFilter = s // GREEN: inside the allowed constructor — must NOT be reported.
	return f
}

// archtestRedFixtureOutsideFilterAssign intentionally assigns `f.wireFilter`
// OUTSIDE the allowed constructor. The S3 typed non-vacuity test must detect
// this as a violation (≥1 diagnostic).
//
//nolint:unused // referenced exclusively by archtest scanner via AST loading.
func archtestRedFixtureOutsideFilterAssign(s string) SubscribableFilter {
	f := SubscribableFilter{}
	f.wireFilter = s // RED: outside the allowed constructor — must be reported.
	return f
}
