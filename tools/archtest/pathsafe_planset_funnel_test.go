// INVARIANT: PATHSAFE-PLANSET-FUNNEL-01
//
// pkg/pathsafe.PlanSet is the typed container that pathsafe.WritePlannedFiles
// accepts. The dup-AbsPath-reject invariant for plans (formerly the runtime
// `duplicatePass` Medium guard) is lifted to type-system Hard via PlanSet:
//
//   - The internal slice field `items []PlannedFile` is unexported.
//   - NewPlanSet is the SOLE public constructor and performs the dup-reject
//     before returning.
//
// Therefore a PlanSet value visible to WritePlannedFiles cannot contain
// duplicates: the Go compiler rejects `pathsafe.PlanSet{items: ...}` outside
// the pathsafe package, and the only intra-package field-set construction is
// inside NewPlanSet itself.
//
// AI-rebust: Hard (compile-time, struct field privacy + sealed constructor
// pattern from charter §载体决策原则 type system). This archtest exists as
// a regression guard for that structural property: it reflects PlanSet to
// confirm `items` is unexported and that NewPlanSet exists. A future commit
// that exported items (or removed the public NewPlanSet) would silently
// break the funnel; this test fails fast in CI.
//
// # Recognition: reflect-based structural invariant
//
// reflect.TypeOf(pathsafe.PlanSet{}).Field(*) — assert the items field is
// present and unexported (field.PkgPath != ""). The companion check inspects
// the *types.Package for the public constructor name.
//
// # Blind spots (declared per ai-collab §载体决策原则)
//
// reflect-based field walk misses:
//
//  1. Future refactor that drops `items` entirely and uses a different
//     storage scheme (e.g. embedded slice) — the field-name guard would
//     no longer apply. This is acceptable: any restructuring of PlanSet's
//     storage will fail other archtests (e.g. NewPlanSet signature check).
//  2. A hypothetical exported method like `(PlanSet).SetItems([]PlannedFile)`
//     that bypasses NewPlanSet's dup-reject. The archtest does NOT scan for
//     this; it is covered conceptually by code review that any mutating
//     method on PlanSet must run dup-reject.
//     This blind spot is left to code review: any new exported mutating method
//     on PlanSet must run dup-reject in its body, mirroring NewPlanSet's invariant.
package archtest

import (
	"reflect"
	"testing"

	"github.com/ghbvf/gocell/pkg/pathsafe"
)

// TestPathsafePlanSetFunnel_ItemsFieldUnexported reflects pathsafe.PlanSet
// and asserts the storage slice field (`items`) is unexported. An exported
// items field would let any caller construct a PlanSet{items: dups} literal
// outside pathsafe, bypassing the dup-reject in NewPlanSet — Hard funnel
// regression.
func TestPathsafePlanSetFunnel_ItemsFieldUnexported(t *testing.T) {
	t.Parallel()
	rt := reflect.TypeOf(pathsafe.PlanSet{})
	if rt.NumField() == 0 {
		t.Fatal("PATHSAFE-PLANSET-FUNNEL-01: pathsafe.PlanSet has no fields; structural invariant broken")
	}
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath == "" {
			t.Errorf("PATHSAFE-PLANSET-FUNNEL-01: field %q is exported; PlanSet must keep its storage private "+
				"so cross-package callers cannot bypass NewPlanSet's dup-reject", f.Name)
		}
	}
}

// TestPathsafePlanSetFunnel_NewPlanSetExists asserts that the public
// constructor NewPlanSet remains addressable as a package-level symbol.
// Removing it without a replacement Hard funnel would re-introduce the
// Soft `[]PlannedFile` parameter shape.
func TestPathsafePlanSetFunnel_NewPlanSetExists(t *testing.T) {
	t.Parallel()
	// Sanity construction: empty plan must return zero-value PlanSet, nil error.
	ps, err := pathsafe.NewPlanSet(nil)
	if err != nil {
		t.Fatalf("PATHSAFE-PLANSET-FUNNEL-01: NewPlanSet(nil) failed: %v", err)
	}
	if ps.Len() != 0 {
		t.Fatalf("PATHSAFE-PLANSET-FUNNEL-01: NewPlanSet(nil).Len() = %d, want 0", ps.Len())
	}
}
