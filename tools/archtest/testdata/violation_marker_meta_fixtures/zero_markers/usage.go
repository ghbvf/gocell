// Package zeromarkers is a meta-fixture for TestCountViolationMarkers_ZeroMarkers:
// no spec.Violation() calls → CountViolationMarkers must return 0.
package zeromarkers

func wholeFile() string { return "no markers" }
