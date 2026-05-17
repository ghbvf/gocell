package pathsafe_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// testNewPlanSet wraps pathsafe.NewPlanSet with t.Fatal on error so existing
// table-driven tests can keep their terse single-line WritePlannedFiles
// invocation idiom after the API switched from []PlannedFile to PlanSet
// (PATHSAFE-PLANSET-TYPED-HARD-01).
func testNewPlanSet(t testing.TB, items []pathsafe.PlannedFile) pathsafe.PlanSet {
	t.Helper()
	ps, err := pathsafe.NewPlanSet(items)
	if err != nil {
		t.Fatalf("NewPlanSet: %v", err)
	}
	return ps
}
