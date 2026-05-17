package assembly

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
	"github.com/ghbvf/gocell/pkg/scaffoldid"
)

// mustPlanSet wraps pathsafe.NewPlanSet with t.Fatal on construction error so
// test bodies can keep their single-line WritePlannedFiles invocation idiom
// after the API switched from []PlannedFile to PlanSet
// (PATHSAFE-PLANSET-TYPED-HARD-01).
func mustPlanSet(t testing.TB, items []pathsafe.PlannedFile) pathsafe.PlanSet {
	t.Helper()
	ps, err := pathsafe.NewPlanSet(items)
	if err != nil {
		t.Fatalf("NewPlanSet: %v", err)
	}
	return ps
}

// mustID wraps scaffoldid.Parse with t.Fatal on validation error so fixture
// construction sites can stay terse. Only call with known-valid literal IDs
// that match AssemblyIDPattern (^[a-z][a-z0-9]+$); invalid strings will cause
// t.Fatal (SCAFFOLD-INPUT-CONTRACT-TYPED-ID-01).
func mustID(t testing.TB, raw string) scaffoldid.ScaffoldID {
	t.Helper()
	id, err := scaffoldid.Parse(raw)
	if err != nil {
		t.Fatalf("scaffoldid.Parse(%q): %v", raw, err)
	}
	return id
}
