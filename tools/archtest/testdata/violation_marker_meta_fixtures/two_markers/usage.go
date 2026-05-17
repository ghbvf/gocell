// Package twomarkers is a meta-fixture for TestCountViolationMarkers_TwoMarkers:
// exactly two spec.Violation() calls → CountViolationMarkers must return 2.
package twomarkers

import spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"

func a() {
	spec.Violation()
}

func b() {
	spec.Violation()
}
