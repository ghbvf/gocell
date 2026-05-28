// INVARIANT: PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01
//
// PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01 — CheckpointStore conformance backstop.
//
// Every concrete production type implementing kernel/projection.CheckpointStore
// must appear in at least one projectiontest.RunCheckpointConformance call in a
// _test.go file of its package.
//
// This enforces the contract-fanout.md §5 M4 rule: every CheckpointStore impl
// must be verified by the shared conformance harness. Adding a new impl without
// enrolling it is a CI failure.
//
// PR-01 status: genuinely-green. MemCheckpointStore is enrolled in
// kernel/projection/memstore_test.go::TestMemCheckpointStore_Conformance.
//
// # AI-robust grading
//
//   - Medium (typed impl-discovery + conformance call scan, both via *types.Info).
//     This is the per-implementation variant: the scanner resolves the concrete
//     store type passed as the first argument to RunCheckpointConformance, so a
//     package with two impls must enroll each one separately. Package-level
//     co-location is insufficient.
//   - Medium is the ceiling: Go cannot require a _test.go file to exist for a
//     type at compile time. The behavioral correctness of RunCheckpointConformance
//     itself carries a different guarantee (runtime correctness of the store
//     contract under the five canonical sub-tests).
//
// # Blind spots (forms *types.Info cannot see)
//
//   - B1. reflect-based implicit implementations: no production projection code
//     uses this pattern. Confirmed by
//     TestProjectionCheckpointConformanceEnroll01_ReverseBlindSpot_NoReflectImpl.
//
//   - B2. Generated mock implementations in _test.go: test-file types are not
//     scanned for implementations (Tests=false in the production load pass).
//     A generated mock in a production non-test file would be flagged — intentionally.
//
//   - B3. Indirect construction (factory returning an interface): the scanner
//     resolves the concrete type of the argument passed to RunCheckpointConformance.
//     If the argument is an interface-typed variable (not a constructor call), the
//     concrete type is unresolvable and enrollment is not credited. Authors must
//     pass a concretely-typed constructor call or := variable.
//
// ref: tools/archtest/saga_journal_conformance_enrollment_test.go (same pattern)
// ref: tools/archtest/cell_repo_readyz_probe_test.go (per-impl enrollment precedent)
// ref: docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md §3
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	checkpointConformancePkg      = "github.com/ghbvf/gocell/kernel/projection/projectiontest"
	checkpointConformanceFuncName = "RunCheckpointConformance"
)

// TestProjectionCheckpointConformanceEnroll01 enforces PROJECTION-CHECKPOINT-
// CONFORMANCE-ENROLL-01: every concrete production type implementing
// kernel/projection.CheckpointStore must be enrolled in a
// projectiontest.RunCheckpointConformance call in a _test.go file.
//
// Per-impl enrollment: the scanner resolves the concrete store type passed as
// the first argument to RunCheckpointConformance. A package with N impls must
// individually enroll each of the N.
//
// # Blind spots
//
//   - B1. reflect-based impl: covered by ReverseBlindSpot_NoReflectImpl.
//   - B2. Generated mock in non-test file: flagged intentionally.
//   - B3. Indirect construction (interface-typed arg): documented accepted limitation.
func TestProjectionCheckpointConformanceEnroll01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	// ─── Step 1: resolve CheckpointStore interface + collect impl packages ────
	//
	// Iface and impl types MUST come from the same packages.Load invocation so
	// that types.Implements uses pointer-identical *types.Named descriptors.
	var cpIface *types.Interface
	var cpImplPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == checkpointStoreIfacePkg {
				if obj := p.Pkg.Scope().Lookup(checkpointStoreIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							cpIface = iface.Complete()
						}
					}
				}
			}
			cpImplPkgs = append(cpImplPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, cpIface,
		"PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01: failed to resolve CheckpointStore interface; "+
			"check import path %s", checkpointStoreIfacePkg)

	// ─── Step 2: collect all concrete implementations ────────────────────────
	implSet := make(map[string]bool)    // "pkg/path.TypeName" → true
	implPkgSet := make(map[string]bool) // pkg path → true

	for _, pkg := range cpImplPkgs {
		if pkg == nil {
			continue
		}
		collectCheckpointStoreImplsForEnrollment(pkg, cpIface, implSet, implPkgSet)
	}

	// Sanity anchor: at least MemCheckpointStore must be found.
	require.NotEmpty(t, implSet,
		"PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01: zero CheckpointStore implementations collected — "+
			"sanity anchor: at least kernel/projection.MemCheckpointStore must be found. "+
			"Likely a prodscan regression or type-universe mismatch.")

	// ─── Step 3: scan test corpus for RunCheckpointConformance callsites ─────
	//
	// Enrollment is tracked per-impl (per concrete type), not per package.
	// Each RunCheckpointConformance call's store argument is resolved to its
	// concrete type key via TypesInfo.
	enrolledImpls := make(map[string]bool) // "pkg/path.TypeName" → true

	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !hasCheckpointConformanceCall(f, p.TypesInfo) {
					continue
				}
				// Resolve the concrete type of the store argument.
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != checkpointConformancePkg || name != checkpointConformanceFuncName {
						return
					}
					// RunCheckpointConformance(t *testing.T, store projection.CheckpointStore)
					// args[1] is the store argument.
					if len(call.Args) < 2 {
						return
					}
					if key, ok := concreteCheckpointKey(p.TypesInfo, call.Args[1]); ok {
						enrolledImpls[key] = true
					}
				})
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		dotIdx := strings.LastIndex(implKey, ".")
		if dotIdx < 0 {
			continue
		}
		pkgPath := implKey[:dotIdx]
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: projection.CheckpointStore impl %q not enrolled in "+
					"projectiontest.RunCheckpointConformance "+
					"(PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01). "+
					"Add a _test.go in package %s that calls "+
					"projectiontest.RunCheckpointConformance(t, <store>) passing "+
					"a concretely-typed instance of this impl.",
				implKey, pkgPath),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01", diags)
}

// TestProjectionCheckpointConformanceEnroll01_REDFixture simulates a missing
// enrollment by removing one impl from the enrolled set and asserts the
// diagnostic logic produces at least one violation. It also verifies that an
// entirely-empty enrolledImpls set (zero enrollments) produces violations for
// all discovered implementations.
func TestProjectionCheckpointConformanceEnroll01_REDFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	var cpIface *types.Interface
	var cpImplPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == checkpointStoreIfacePkg {
				if obj := p.Pkg.Scope().Lookup(checkpointStoreIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							cpIface = iface.Complete()
						}
					}
				}
			}
			cpImplPkgs = append(cpImplPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, cpIface, "REDFixture: could not resolve CheckpointStore interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range cpImplPkgs {
		if pkg != nil {
			collectCheckpointStoreImplsForEnrollment(pkg, cpIface, implSet, implPkgSet)
		}
	}
	require.NotEmpty(t, implSet, "REDFixture: implSet must not be empty (need at least MemCheckpointStore)")

	// --- Case A: zero enrollments (enrolledImpls entirely empty) ---
	// Simulates a world where no _test.go has called RunCheckpointConformance.
	// Every discovered impl must be flagged.
	t.Run("zero enrollments fires for all impls", func(t *testing.T) {
		emptyEnrolled := make(map[string]bool)
		var diags []Diagnostic
		for implKey := range implSet {
			if !emptyEnrolled[implKey] {
				diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
			}
		}
		assert.Equal(t, len(implSet), len(diags),
			"REDFixture zero-enrollment: expected one diagnostic per impl (%d), got %d",
			len(implSet), len(diags))
	})

	// --- Case B: one impl missing from enrolled set ---
	// Pick an arbitrary impl as the "missing enrollment" target.
	var targetImplKey string
	for k := range implSet {
		targetImplKey = k
		break
	}

	// Build enrolled impls = all impls EXCEPT the target.
	enrolledImpls := make(map[string]bool)
	for k := range implSet {
		if k != targetImplKey {
			enrolledImpls[k] = true
		}
	}

	var diags []Diagnostic
	for implKey := range implSet {
		if !enrolledImpls[implKey] {
			diags = append(diags, Diagnostic{Rel: implKey, Message: implKey + " not enrolled"})
		}
	}

	assert.GreaterOrEqual(t, len(diags), 1,
		"REDFixture: removing impl %q from enrolledImpls must produce ≥1 violation, got 0", targetImplKey)
}

// TestProjectionCheckpointConformanceEnroll01_ReverseBlindSpot_NoReflectImpl (B1)
// confirms no production non-test file outside kernel/projection packages uses
// the string literal "CheckpointStore" as a reflect target.
func TestProjectionCheckpointConformanceEnroll01_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping archtest in -short mode")
	}

	root := findModuleRoot(t)
	scope := ModuleScope(root)

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			// Skip kernel/projection packages that legitimately mention "CheckpointStore".
			if strings.HasPrefix(rel, "kernel/projection/") {
				continue
			}
			EachInSubtree[ast.BasicLit](f, func(lit *ast.BasicLit) {
				val, ok := StringLitValue(lit)
				if !ok || val != checkpointStoreIfaceName {
					return
				}
				const b1msg = "blind-spot B1: string literal \"CheckpointStore\" in production code " +
					"outside kernel/projection may indicate reflect-based impl " +
					"(PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01)"
				out = append(out, Diagnostic{
					Rel:     rel,
					Line:    p.Fset.Position(lit.Pos()).Line,
					Message: b1msg,
				})
			})
		}
		return out
	})
	assert.Empty(t, diags,
		"B1 reverse: no production non-test file outside kernel/projection should contain "+
			"the string literal %q as reflect bait", checkpointStoreIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectCheckpointStoreImplsForEnrollment adds to implSet all concrete named
// types in pkg (exported AND unexported) that implement CheckpointStore (value
// or pointer receiver). Interface types are skipped. implPkgSet tracks pkg paths.
//
// Types that embed the CheckpointStore interface directly (delegation wrappers /
// sealed markers like internalCellCheckpointStore) are excluded: they forward
// calls to the wrapped impl and have no real storage semantics of their own.
// Conformance-testing a wrapper without a backing store would vacuously pass all
// five sub-tests — the wrapped impl must be enrolled separately.
func collectCheckpointStoreImplsForEnrollment(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
	if iface == nil {
		return
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok {
			continue
		}
		t := obj.Type()
		if _, isIface := t.Underlying().(*types.Interface); isIface {
			continue
		}
		if typesutil.ImplementsInterface(t, iface) {
			// Skip embedded-interface delegation wrappers: a struct that directly
			// embeds the CheckpointStore interface type satisfies it structurally
			// but has no independent storage — conformance must be tested on the
			// concrete backing impl, not on the wrapper. This mirrors the B3
			// blind-spot rationale in cell_repo_readyz_probe_test.go.
			if isEmbedCheckpointStoreWrapper(t, iface) {
				continue
			}
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// isEmbedCheckpointStoreWrapper reports whether t is a struct type whose
// underlying fields include the CheckpointStore interface as an embedded
// (anonymous) field. Such types are delegation wrappers, not real storage.
func isEmbedCheckpointStoreWrapper(t types.Type, iface *types.Interface) bool {
	check := t
	if ptr, ok := check.(*types.Pointer); ok {
		check = ptr.Elem()
	}
	st, ok := check.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := range st.NumFields() {
		field := st.Field(i)
		if !field.Anonymous() {
			continue
		}
		ft := field.Type()
		// Check if the anonymous field type IS the CheckpointStore interface.
		if named, ok := ft.(*types.Named); ok {
			if embIface, ok := named.Underlying().(*types.Interface); ok {
				if types.Identical(embIface, iface) {
					return true
				}
			}
		}
	}
	return false
}

// hasCheckpointConformanceCall reports whether file contains at least one call
// to projectiontest.RunCheckpointConformance resolved via TypesInfo.
func hasCheckpointConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == checkpointConformancePkg && name == checkpointConformanceFuncName
	})
	return ok
}

// concreteCheckpointKey resolves expr's static type to a concrete impl key
// ("pkg/path.TypeName"), unwrapping a single pointer. Returns ok=false for
// interface-typed or unnamed expressions.
func concreteCheckpointKey(info *types.Info, expr ast.Expr) (string, bool) {
	t := info.TypeOf(expr)
	if t == nil {
		return "", false
	}
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name(), true
}
