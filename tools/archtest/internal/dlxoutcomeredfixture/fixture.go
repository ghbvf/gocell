//go:build archtest_fixture

// Package dlxoutcomeredfixture is the red fixture for
// MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01's H2 and H4 scanner-fires self-checks. It
// declares a REPLICA of adapters/mqtt/internal/dlxoutcome.Outcome (an exported
// struct with only a blank/unexported field — the sealed-proof-token shape) and
// plants the three zero-value forge forms H2 must detect: an empty composite
// literal, a zero-value `var` declaration, and a `*new(T)` allocation.
//
// The same three planted functions double as H4 (sole-producer) red samples:
// each returns the replica Outcome but is NOT named Dropped/Captured, so the H4
// producer-allowlist scan must flag all of them as non-sanctioned producers.
//
// Why a replica and not the real dlxoutcome.Outcome: dlxoutcome lives under
// adapters/mqtt/internal, and Go's internal-package rule forbids this tools-module
// package from importing it. The replica lets the H2 scanner exercise the same
// go/types composite-literal / var type-resolution path it runs over production —
// which is the structural invariant H2 relies on. (Same replica rationale as
// internal/mqttredfixture, whose real type is unexported-field-sealed.)
//
// Why not in-package (adapters/mqtt itself): the path-scoped allowlist guard
// (#944) rejects the archtest_fixture tag in production paths because a production
// file silently skipped by the default-tag scan is a fail-open vector. The
// internal/ subtree here is the sanctioned home.
//
// ref: tools/archtest/mqtt_dlx_failure_signal_funnel_test.go TestMQTTDLXFailureSignalFunnel_H2_ScannerFiresOnRedFixture
// ref: tools/archtest/internal/mqttredfixture/fixture.go (replica-fixture precedent)
// ref: gh #1440 — MQTT-DLX-FAILURE-SIGNAL-FUNNEL-01 sealed-construction funnel (Medium, gh #1873 F1)
package dlxoutcomeredfixture

// FixtureOutcome is a sealed-shape replica of dlxoutcome.Outcome: an exported
// struct whose only field is a blank field of an unexported type. The H2 scanner
// resolves composite-literal and var types by (package, name), so this replica
// drives the identical resolution path as the real Outcome.
type FixtureOutcome struct{ _ recordedToken }

type recordedToken struct{}

// archtestForgedOutcomeLiteral plants the empty composite-literal forge. The H2
// scanner must report it — no consumer may mint an Outcome without going through a
// recording constructor.
//
//nolint:unused // referenced exclusively by the archtest scanner via AST loading.
func archtestForgedOutcomeLiteral() FixtureOutcome {
	return FixtureOutcome{}
}

// archtestForgedOutcomeVar plants the zero-value `var` forge — invisible to a
// composite-literal-only scan, which is exactly the second form H2 closes.
//
//nolint:unused // referenced exclusively by the archtest scanner via AST loading.
func archtestForgedOutcomeVar() FixtureOutcome {
	var o FixtureOutcome
	return o
}

// archtestForgedOutcomeNew plants the `*new(T)` allocation forge — a zero Outcome
// produced by the builtin new, with neither a composite literal nor a typed
// zero-var, so a composite-lit/var-only scan misses it. This is the third form H2
// closes (gh #1873 review F1: new(T) is detectable, not an irreducible residual).
//
//nolint:unused // referenced exclusively by the archtest scanner via AST loading.
func archtestForgedOutcomeNew() FixtureOutcome {
	return *new(FixtureOutcome)
}
