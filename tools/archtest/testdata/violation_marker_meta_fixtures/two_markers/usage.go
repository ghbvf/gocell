// Package twomarkers is a meta-fixture for TestCountViolationMarkers_TwoMarkers:
// exactly two spec.Violation() calls → CountViolationMarkers must return 2.
//
// No //go:build directive: this meta-fixture is loaded via RunTyped with an
// explicit package pattern (not RunTypedFixture), so no build tag is required.
// Adding //go:build archtest_fixture here would introduce bootstrap circularity
// — the meta-fixture tests CountViolationMarkers itself, which is the mechanism
// that interprets the build tag for production fixtures. Standalone loading via
// RunTyped avoids the circular dependency.
package twomarkers

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

func a() {
	spec.Violation()
}

func b() {
	spec.Violation()
}
