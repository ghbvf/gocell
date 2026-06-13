//go:build archtest_fixture

// Package projectionappendfixture is the RED fixture for
// PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01. The real append target
// (adapters/postgres.journalingOutboxWriter.appendProjectionEvents) is UNEXPORTED,
// so a fixture package cannot call it — Go visibility is the upstream Hard rung.
// To exercise the downstream whole-package caller-allowlist (closing the "another
// same-package function calls appendProjectionEvents" blind spot), this fixture
// reproduces the exact shape: an unexported appendProjectionEvents method with one
// SANCTIONED chokepoint caller (journalProjectionSubset) and one UNSANCTIONED
// bypass caller. The detector is parameterized by package path, so the test runs it
// against this fixture's path + the fixture chokepoint allowlist and asserts it
// fires exactly once (the bypass), proving the funnel is callsite-level, not
// file-level.
package projectionappendfixture

import "context"

type fakeJournalingWriter struct{}

// appendProjectionEvents stands in for the real unexported journal append.
func (w *fakeJournalingWriter) appendProjectionEvents(_ context.Context) error { return nil }

// journalProjectionSubset is the sanctioned chokepoint (allowlisted in the test).
func (w *fakeJournalingWriter) journalProjectionSubset(ctx context.Context) error {
	return w.appendProjectionEvents(ctx)
}

// bypassAppend is an UNSANCTIONED same-package caller — the detector must flag it.
func (w *fakeJournalingWriter) bypassAppend(ctx context.Context) error {
	return w.appendProjectionEvents(ctx)
}
