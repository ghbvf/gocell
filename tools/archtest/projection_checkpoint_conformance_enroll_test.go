// INVARIANT: PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01
//
// PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01 — CheckpointStore conformance backstop.
//
// Every concrete production type implementing kernel/projection.CheckpointStore
// must appear in at least one projectiontest.RunCheckpointConformance call in a
// _test.go file somewhere in the production test corpus. Enrollment is credited
// by the impl's CONCRETE TYPE (the resolved type of the call's store argument),
// not by the enrolling test's package — this matches the established sibling
// precedents (saga_journal_conformance_enrollment + cell_repo_readyz_probe), which
// also key enrollment by concrete type across the corpus. (Earlier godoc said "of
// its package"; that over-promised a package-scope check the scanner never
// performed — reconciled to doc==code rather than diverging from the siblings.)
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
//   - Negative coverage: the Medium guard's detection path (impl discovery +
//     RunCheckpointConformance arg-type resolution + the shared
//     unenrolledCheckpointDiags comparison) is exercised against a real fixture by
//     TestProjectionCheckpointConformanceEnroll01_RedFixture — a scanner regression
//     fails CI rather than degrading silently (F8: the prior REDFixture only
//     re-implemented the diff loop on in-memory maps and proved nothing about the
//     scanner). Enrollment is keyed by the impl's concrete type across the whole
//     test corpus (not "of its package"), consistent with the saga / cell_repo_readyz
//     sibling enrollment archtests.
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
// ref: tools/archtest/saga_invariants_test.go §SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01 (same pattern)
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

// fixtureEnrollPkg is the import path of the archtest_fixture-gated red fixture
// loaded by TestProjectionCheckpointConformanceEnroll01_RedFixture. The fixture
// declares an enrolledStore (enrolled via RunCheckpointConformance in its
// _test.go) and an unenrolledStore (deliberately not enrolled).
const fixtureEnrollPkg = "github.com/ghbvf/gocell/tools/archtest/internal/projectioncheckpointenrollfixture"

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

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
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

	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, prodPatterns),
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

				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != checkpointConformancePkg || name != checkpointConformanceFuncName {
						return
					}

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

	// ─── Step 4: flag unenrolled implementations (shared diff) ───────────────
	// The diff lives in unenrolledCheckpointDiags so that the RED fixture test
	// exercises the SAME production comparison logic (not a re-implemented copy).
	Report(t, "PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01",
		unenrolledCheckpointDiags(implSet, enrolledImpls))
}

// unenrolledCheckpointDiags returns one Diagnostic per CheckpointStore impl in
// implSet that is absent from enrolledImpls. It is the single source of the
// enroll-rule comparison, shared by TestProjectionCheckpointConformanceEnroll01
// (production) and TestProjectionCheckpointConformanceEnroll01_RedFixture (so the
// negative case verifies the real diff, not an inline re-implementation).
func unenrolledCheckpointDiags(implSet, enrolledImpls map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: projection.CheckpointStore impl %q not enrolled in "+
					"projectiontest.RunCheckpointConformance "+
					"(PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01). Add a _test.go that "+
					"calls projectiontest.RunCheckpointConformance(t, <store>) passing a "+
					"concretely-typed instance of this impl.",
				implKey,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// TestProjectionCheckpointConformanceEnroll01_RedFixture runs the REAL scanner
// over a synthetic fixture (internal/projectioncheckpointenrollfixture) instead
// of manipulating in-memory maps. The fixture declares two CheckpointStore impls:
// enrolledStore (enrolled via a RunCheckpointConformance call in the fixture's
// _test.go) and unenrolledStore (no enrollment). The test exercises the actual
// detection path end-to-end:
//
//   - impl discovery via collectCheckpointStoreImplsForEnrollment + ImplementsInterface
//     (finds both fixture impls),
//   - enrollment-call resolution via hasCheckpointConformanceCall + ResolvePackageRef
//   - concreteCheckpointKey (credits enrolledStore from the real call expression),
//   - the shared unenrolledCheckpointDiags comparison (same code as the production rule).
//
// It asserts the scanner FLAGS unenrolledStore (negative direction) and does NOT
// flag enrolledStore (positive direction) — so a regression in any of the real
// scanner steps fails the test, unlike the previous in-memory map re-implementation.
func TestProjectionCheckpointConformanceEnroll01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	// kernel/projection is loaded alongside the fixture as an explicit root so the
	// CheckpointStore interface and the fixture impls come from the SAME load
	// (pointer-identical *types.Named, required by types.Implements).
	loadPatterns := []string{
		"./kernel/projection/...",
		"./tools/archtest/internal/projectioncheckpointenrollfixture/...",
	}

	// ─── Pass 1 (Tests:false): resolve iface + collect impls in one load ─────
	var cpIface *types.Interface
	var cpImplPkgs []*types.Package
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, loadPatterns),
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

	require.NotNil(t, cpIface, "RedFixture: could not resolve CheckpointStore interface from fixture load")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range cpImplPkgs {
		if pkg != nil {
			collectCheckpointStoreImplsForEnrollment(pkg, cpIface, implSet, implPkgSet)
		}
	}

	// Restrict to the fixture package's impls — kernel/projection.MemCheckpointStore
	// is also discovered (as a load root) but is irrelevant to this fixture's assertions.
	for k := range implSet {
		if !strings.HasPrefix(k, fixtureEnrollPkg+".") {
			delete(implSet, k)
		}
	}
	unenrolledKey := fixtureEnrollPkg + ".unenrolledStore"
	enrolledKey := fixtureEnrollPkg + ".enrolledStore"
	require.Contains(t, implSet, unenrolledKey, "RedFixture: impl discovery must find unenrolledStore")
	require.Contains(t, implSet, enrolledKey, "RedFixture: impl discovery must find enrolledStore")

	// ─── Pass 2 (Tests:true): scan the fixture's _test.go for enrollment ─────
	enrolledImpls := make(map[string]bool)
	_ = Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{"./tools/archtest/internal/projectioncheckpointenrollfixture/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
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
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != checkpointConformancePkg || name != checkpointConformanceFuncName {
						return
					}
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

	require.True(t, enrolledImpls[enrolledKey],
		"RedFixture: the enrollment scan must credit enrolledStore via its "+
			"RunCheckpointConformance call (positive direction: arg-type resolution works)")

	// ─── Shared diff: the SAME production function the GREEN test uses ───────
	diags := unenrolledCheckpointDiags(implSet, enrolledImpls)

	var flaggedUnenrolled, flaggedEnrolled bool
	for _, d := range diags {
		switch d.Rel {
		case unenrolledKey:
			flaggedUnenrolled = true
		case enrolledKey:
			flaggedEnrolled = true
		}
	}
	assert.True(t, flaggedUnenrolled,
		"RedFixture: the real scanner must FLAG unenrolledStore (negative direction)")
	assert.False(t, flaggedEnrolled,
		"RedFixture: the real scanner must NOT flag enrolledStore (its RunCheckpointConformance "+
			"call was resolved and credited)")
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

	diags := Run(t, AST(scope), func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}

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
