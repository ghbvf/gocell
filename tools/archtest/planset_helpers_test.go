// planset_helpers_test.go — shared archtest fixture helpers reused by
// archtest funnel tests that exercise pathsafe.PlanSet construction and
// scaffoldid.ScaffoldID validation.
//
// invariants asserted in this file (none — pure helper module):
//   - INVARIANT: PATHSAFE-PLANSET-FUNNEL-01 (shared mustPlanSet helper)
//   - INVARIANT: SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01 (shared mustID helper)
package archtest

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// mustPlanSet wraps pathsafe.NewPlanSet with t.Fatal on construction error so
// archtest fixture bodies that drive pathsafe.WritePlannedFiles can keep their
// single-line invocation idiom after the API switched from []PlannedFile to
// PlanSet (PATHSAFE-PLANSET-TYPED-HARD-01).
func mustPlanSet(t testing.TB, items []pathsafe.PlannedFile) pathsafe.PlanSet {
	t.Helper()
	ps, err := pathsafe.NewPlanSet(items)
	if err != nil {
		t.Fatalf("NewPlanSet: %v", err)
	}
	return ps
}
