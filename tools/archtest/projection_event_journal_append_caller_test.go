//go:build archtest

// INVARIANT: PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01
//
// This _test.go dogfoods + precision-gates the rule. The importable rule body
// (CheckProjectionEventJournalAppendCaller01 + scanJournalAppendCallers + the full
// package godoc) lives in the non-test companion
// projection_event_journal_append_caller.go — single source, no parallel rule body.
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestProjectionEventJournalAppendCaller01 asserts that every production reference
// to adapters/postgres.appendProjectionEvents originates from the single sanctioned
// chokepoint (journalProjectionSubset), and that the allowlist entry is not stale
// (anti-vacuity reverse check). Dogfoods GoCell itself via the importable rule body.
func TestProjectionEventJournalAppendCaller01(t *testing.T) {
	t.Parallel()
	Report(t, "PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01",
		CheckProjectionEventJournalAppendCaller01(t, ConfigForExternalCell{}))
}

// projectionAppendFixturePkg is the import path of the RED fixture package.
const projectionAppendFixturePkg = PlatformModulePath + "/tools/archtest/internal/projectionappendfixture"

// projectionAppendFixtureAllowlist sanctions only the fixture's chokepoint, so the
// fixture's bypassAppend caller is the single expected violation.
var projectionAppendFixtureAllowlist = map[string]struct{}{
	"(*" + projectionAppendFixturePkg + ".fakeJournalingWriter).journalProjectionSubset": {},
}

// TestProjectionEventJournalAppendCaller01_RedFixture is the negative control: the
// detector, run against a self-contained reproduction of the unexported-append
// shape (appendProjectionEvents cannot be called from outside adapters/postgres, so
// the real symbol cannot be forged in a fixture), must fire EXACTLY once — on the
// unsanctioned bypassAppend caller — proving the downstream whole-package
// caller-allowlist closes the same-package bypass blind spot.
func TestProjectionEventJournalAppendCaller01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var found int
	throwaway := map[string]struct{}{}
	_ = Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/projectionappendfixture"}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			found += len(scanJournalAppendCallers(
				p, projectionAppendFixturePkg, journalAppendFunc, projectionAppendFixtureAllowlist, throwaway))
			return nil
		})
	assert.Equal(t, 1, found,
		"PROJECTION-EVENT-JOURNAL-APPEND-CALLER-01 RED fixture self-check FAILED: expected "+
			"exactly 1 violation (bypassAppend calls appendProjectionEvents from an "+
			"unsanctioned same-package caller). Got %d — found<1 means the scanner missed the "+
			"in-package method call (whole-package downstream regression); found>1 means "+
			"over-detection. This fixture is the negative control for the Hard/Hard funnel.", found)
}
