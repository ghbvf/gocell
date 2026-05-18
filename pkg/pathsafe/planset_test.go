package pathsafe_test

// RED tests for:
//   - PATHSAFE-PLANSET-TYPED-HARD-01: PlanSet typed container with dup-reject
//     in NewPlanSet constructor (items field private → caller cannot bypass).
//   - PATHSAFE-FORCEOVERWRITE-TYPED-CTOR-01: DerivedOverwrite typed ctor as
//     the sole public path that returns a PlannedFile with forceOverwrite
//     semantics (PlannedFile.forceOverwrite is now unexported).
//
// Wave-1 RED: this file does not compile until pathsafe exposes PlanSet,
// NewPlanSet, (PlanSet).Paths, (PlanSet).Len, and DerivedOverwrite, and
// WritePlannedFiles' second parameter has been changed to PlanSet.

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/pathsafe"
)

func requireConflict(t *testing.T, err error, ctx string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected ErrConflict, got nil", ctx)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("%s: err=%v is not *errcode.Error", ctx, err)
	}
	if ec.Code != errcode.ErrConflict {
		t.Fatalf("%s: code=%q, want %q", ctx, ec.Code, errcode.ErrConflict)
	}
}

// NewPlanSet rejects duplicate AbsPath entries with ErrConflict, lifting the
// dup-reject invariant from a runtime Medium guard (duplicatePass) to a
// type-system Hard guarantee (no PlanSet seen by WritePlannedFiles can carry
// duplicates).
func TestNewPlanSet_DuplicateAbsPath_Rejected(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	absA := filepath.Join(realRoot, "a.txt")
	items := []pathsafe.PlannedFile{
		{AbsPath: absA, Content: []byte("first")},
		{AbsPath: absA, Content: []byte("second")},
	}
	_, err = pathsafe.NewPlanSet(items)
	requireConflict(t, err, "NewPlanSet duplicate")
}

// NewPlanSet accepts empty plan and the resulting PlanSet drives a no-op
// WritePlannedFiles (preserves the documented "empty plan = no-op" contract).
func TestNewPlanSet_Empty_NoOp(t *testing.T) {
	t.Parallel()
	ps, err := pathsafe.NewPlanSet(nil)
	if err != nil {
		t.Fatalf("NewPlanSet(nil): %v", err)
	}
	if ps.Len() != 0 {
		t.Fatalf("empty PlanSet.Len() = %d, want 0", ps.Len())
	}
	if got := ps.Paths(); len(got) != 0 {
		t.Fatalf("empty PlanSet.Paths() = %v, want []", got)
	}
	root := t.TempDir()
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	if err := pathsafe.WritePlannedFiles(realRoot, ps, false); err != nil {
		t.Fatalf("WritePlannedFiles(empty PlanSet): %v", err)
	}
}

// (PlanSet).Paths returns AbsPaths in plan order — replaces the deleted
// standalone PlannedPaths([]PlannedFile) helper. Internal slice is private so
// callers can only read paths through this method.
func TestPlanSet_Paths_PreservesOrder(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	abs1 := filepath.Join(realRoot, "z.txt")
	abs2 := filepath.Join(realRoot, "a.txt")
	abs3 := filepath.Join(realRoot, "m.txt")
	ps, err := pathsafe.NewPlanSet([]pathsafe.PlannedFile{
		{AbsPath: abs1, Content: []byte("z")},
		{AbsPath: abs2, Content: []byte("a")},
		{AbsPath: abs3, Content: []byte("m")},
	})
	if err != nil {
		t.Fatalf("NewPlanSet: %v", err)
	}
	got := ps.Paths()
	want := []string{abs1, abs2, abs3}
	if len(got) != len(want) {
		t.Fatalf("Paths len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// DerivedOverwrite is the SOLE public constructor for a PlannedFile that
// bypasses conflict rejection. Caller-side governance gate
// (governance.IsGoCellGenerated) must still be enforced upstream — pathsafe
// remains content-neutral; archtest SCAFFOLD-DERIVED-FORCEOVERWRITE-01 covers
// the cellgen call-site for the gate.
func TestDerivedOverwrite_OverwritesExistingFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	abs := filepath.Join(realRoot, "regen.go")
	if err := os.WriteFile(abs, []byte("// old\n"), 0o644); err != nil {
		t.Fatalf("seed old file: %v", err)
	}
	pf := pathsafe.DerivedOverwrite(abs, []byte("// regenerated\n"))
	ps, err := pathsafe.NewPlanSet([]pathsafe.PlannedFile{pf})
	if err != nil {
		t.Fatalf("NewPlanSet: %v", err)
	}
	if err := pathsafe.WritePlannedFiles(realRoot, ps, false); err != nil {
		t.Fatalf("WritePlannedFiles: %v", err)
	}
	got, err := os.ReadFile(abs) //nolint:gosec // G304: tempdir fixture, path constructed in-test
	if err != nil {
		t.Fatalf("ReadFile after overwrite: %v", err)
	}
	if string(got) != "// regenerated\n" {
		t.Fatalf("after overwrite: got %q, want %q", got, "// regenerated\n")
	}
}

// A plain PlannedFile constructed via struct-literal (no DerivedOverwrite) does
// NOT carry forceOverwrite semantics — conflictPass rejects existing target.
// This is the property that makes "force overwrite" reachable only through
// DerivedOverwrite (forceOverwrite field unexported package-externally).
func TestPlainPlannedFile_DoesNotForceOverwrite(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realRoot, err := pathsafe.ResolveRoot(root)
	if err != nil {
		t.Fatalf("ResolveRoot: %v", err)
	}
	abs := filepath.Join(realRoot, "existing.txt")
	if err := os.WriteFile(abs, []byte("preexisting"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ps, err := pathsafe.NewPlanSet([]pathsafe.PlannedFile{
		{AbsPath: abs, Content: []byte("new")},
	})
	if err != nil {
		t.Fatalf("NewPlanSet: %v", err)
	}
	err = pathsafe.WritePlannedFiles(realRoot, ps, false)
	requireConflict(t, err, "plain PlannedFile over existing file")
}
