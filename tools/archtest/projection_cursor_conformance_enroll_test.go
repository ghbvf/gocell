//go:build archtest

// INVARIANT: PROJECTION-CURSOR-CONFORMANCE-ENROLL-01
//
// PROJECTION-CURSOR-CONFORMANCE-ENROLL-01 — Cursor conformance backstop.
//
// Every concrete production type implementing kernel/projection.Cursor must
// appear as an argument to a projectiontest.RunCursorConformance(...) call in a
// _test.go file somewhere in the production test corpus. Enrollment is credited
// by the impl's CONCRETE TYPE (the resolved type of the call's cursor argument),
// not by the enrolling test's package — consistent with the sibling
// PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01 and
// PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01.
//
// This enforces the contract-fanout.md §5 M4 rule: every Cursor impl must be
// verified by the shared conformance harness against the four position
// invariants documented on projection.Cursor (1-based / monotonic / gap-allowed /
// permanent-error). Adding a new impl without enrolling it is a CI failure.
//
// Companion to the production PG journal-backed Cursor (postgres.
// PGProjectionEventSource, EPIC #1504) alongside MemCursor (#1483), so a machine
// guard that every Cursor impl is conformance-verified is load-bearing.
//
// # AI-robust grading
//
//   - Medium (typed impl-discovery + conformance call scan, both via *types.Info).
//     Same permanent ceiling as the two sibling enroll archtests: Go cannot
//     require a _test.go file to exist for a type at compile time. The Hard path
//     (codegen enumeration of Cursor impls + golden) is over-engineering for the
//     current 2-impl set and would diverge from the siblings; if projection impls
//     proliferate it is upgraded uniformly with them (gh-tracked alongside the
//     siblings). The behavioral correctness of RunCursorConformance itself is a
//     separate guarantee (exercised by MemCursor + the PG integration test).
//   - Negative coverage: the Medium guard's detection path is exercised against a
//     real fixture by TestProjectionCursorConformanceEnroll01_RedFixture.
//
// # Blind spots (forms *types.Info cannot see)
//
//   - B1. reflect-based implicit implementations: no production projection code
//     uses this pattern. UNLIKE the ReplaySource sibling, no automated
//     literal-scan reverse self-check is provided here: the interface token
//     "Cursor" is a common substring across the corpus (pagination "nextCursor",
//     SQL cursors, list-response cursors), so scanning for the bare literal would
//     produce overwhelming false positives. The blind spot is the same
//     empty-in-corpus reflect-impl vector as the sibling; the realistic vector
//     (a normally-typed unenrolled impl) is proven detectable by the RED fixture
//     below. Documented, not auto-enforced.
//   - B2. Generated mock implementations in _test.go: test-file types are not
//     scanned for implementations (Tests=false in the production load pass).
//   - B3. Indirect construction (factory returning an interface): the scanner
//     resolves the concrete type of the argument passed to RunCursorConformance.
//     If the argument is an interface-typed variable, enrollment is not credited.
//
// ref: tools/archtest/projection_replay_source_conformance_enroll_test.go (same pattern)
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
	cursorConformancePkg      = PlatformModulePath + "/kernel/projection/projectiontest"
	cursorConformanceFuncName = "RunCursorConformance"

	cursorIfacePkg  = PlatformModulePath + "/kernel/projection"
	cursorIfaceName = "Cursor"

	fixtureCursorEnrollPkg = PlatformModulePath + "/tools/archtest/internal/projectioncursorenrollfixture"
)

// TestProjectionCursorConformanceEnroll01 enforces PROJECTION-CURSOR-CONFORMANCE-
// ENROLL-01: every concrete production type implementing kernel/projection.Cursor
// must be enrolled in a projectiontest.RunCursorConformance call in a _test.go
// file.
//
// Per-impl enrollment: the scanner resolves the concrete cursor type passed as
// the first non-t argument to RunCursorConformance. A package with N impls must
// individually enroll each.
func TestProjectionCursorConformanceEnroll01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	// ─── Step 1: resolve Cursor interface + collect impl packages ────────────
	var curIface *types.Interface
	var curImplPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == cursorIfacePkg {
				if obj := p.Pkg.Scope().Lookup(cursorIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							curIface = iface.Complete()
						}
					}
				}
			}
			curImplPkgs = append(curImplPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, curIface,
		"PROJECTION-CURSOR-CONFORMANCE-ENROLL-01: failed to resolve Cursor interface; "+
			"check import path %s", cursorIfacePkg)

	// ─── Step 2: collect all concrete implementations ────────────────────────
	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range curImplPkgs {
		if pkg == nil {
			continue
		}
		collectCursorImplsForEnrollment(pkg, curIface, implSet, implPkgSet)
	}

	require.NotEmpty(t, implSet,
		"PROJECTION-CURSOR-CONFORMANCE-ENROLL-01: zero Cursor implementations collected — "+
			"sanity anchor: at least kernel/projection.MemCursor must be found.")

	// ─── Step 3: scan test corpus for RunCursorConformance callsites ──────────
	enrolledImpls := make(map[string]bool)

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
				if !hasCursorConformanceCall(f, p.TypesInfo) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != cursorConformancePkg || name != cursorConformanceFuncName {
						return
					}
					if len(call.Args) < 2 {
						return
					}
					if key, ok := concreteCursorKey(p.TypesInfo, call.Args[1]); ok {
						enrolledImpls[key] = true
					}
				})
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	Report(t, "PROJECTION-CURSOR-CONFORMANCE-ENROLL-01",
		unenrolledCursorDiags(implSet, enrolledImpls))
}

// unenrolledCursorDiags returns one Diagnostic per Cursor impl in implSet that is
// absent from enrolledImpls. Shared by both the production test and the RED
// fixture test.
func unenrolledCursorDiags(implSet, enrolledImpls map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: projection.Cursor impl %q not enrolled in "+
					"projectiontest.RunCursorConformance "+
					"(PROJECTION-CURSOR-CONFORMANCE-ENROLL-01). Add a _test.go that "+
					"calls projectiontest.RunCursorConformance(t, <cursor>, seed, newUnseeded) passing a "+
					"concretely-typed instance of this impl.",
				implKey,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// TestProjectionCursorConformanceEnroll01_RedFixture runs the REAL scanner over
// the synthetic fixture (internal/projectioncursorenrollfixture) to verify:
//   - enrolledCursor is NOT flagged (enrolled via RunCursorConformance)
//   - unenrolledCursor IS flagged (no enrollment call)
func TestProjectionCursorConformanceEnroll01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	loadPatterns := []string{
		"./kernel/projection/...",
		"./tools/archtest/internal/projectioncursorenrollfixture/...",
	}

	// ─── Pass 1 (Tests:false): resolve iface + collect impls ─────────────────
	var curIface *types.Interface
	var curImplPkgs []*types.Package
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, loadPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == cursorIfacePkg {
				if obj := p.Pkg.Scope().Lookup(cursorIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							curIface = iface.Complete()
						}
					}
				}
			}
			curImplPkgs = append(curImplPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, curIface, "RedFixture: could not resolve Cursor interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range curImplPkgs {
		if pkg != nil {
			collectCursorImplsForEnrollment(pkg, curIface, implSet, implPkgSet)
		}
	}

	// Restrict to fixture impls only.
	for k := range implSet {
		if !strings.HasPrefix(k, fixtureCursorEnrollPkg+".") {
			delete(implSet, k)
		}
	}
	unenrolledKey := fixtureCursorEnrollPkg + ".unenrolledCursor"
	enrolledKey := fixtureCursorEnrollPkg + ".enrolledCursor"
	require.Contains(t, implSet, unenrolledKey, "RedFixture: impl discovery must find unenrolledCursor")
	require.Contains(t, implSet, enrolledKey, "RedFixture: impl discovery must find enrolledCursor")

	// ─── Pass 2 (Tests:true): scan fixture _test.go for enrollment ───────────
	enrolledImpls := make(map[string]bool)
	_ = Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{"./tools/archtest/internal/projectioncursorenrollfixture/..."}),

		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !hasCursorConformanceCall(f, p.TypesInfo) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != cursorConformancePkg || name != cursorConformanceFuncName {
						return
					}
					if len(call.Args) < 2 {
						return
					}
					if key, ok := concreteCursorKey(p.TypesInfo, call.Args[1]); ok {
						enrolledImpls[key] = true
					}
				})
			}
			return nil
		})

	require.True(t, enrolledImpls[enrolledKey],
		"RedFixture: the enrollment scan must credit enrolledCursor via its "+
			"RunCursorConformance call (positive direction)")

	diags := unenrolledCursorDiags(implSet, enrolledImpls)

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
		"RedFixture: the real scanner must FLAG unenrolledCursor (negative direction)")
	assert.False(t, flaggedEnrolled,
		"RedFixture: the real scanner must NOT flag enrolledCursor")
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectCursorImplsForEnrollment adds to implSet all concrete named types in
// pkg that implement Cursor (value or pointer receiver). Interface types and
// embedded-interface delegation wrappers are excluded.
func collectCursorImplsForEnrollment(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
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
			if isEmbedCursorWrapper(t, iface) {
				continue
			}
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// isEmbedCursorWrapper reports whether t is a struct that embeds the Cursor
// interface as an anonymous field (delegation wrapper).
func isEmbedCursorWrapper(t types.Type, iface *types.Interface) bool {
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

// hasCursorConformanceCall reports whether file contains at least one call to
// projectiontest.RunCursorConformance.
func hasCursorConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == cursorConformancePkg && name == cursorConformanceFuncName
	})
	return ok
}

// concreteCursorKey resolves expr's static type to a concrete impl key
// ("pkg/path.TypeName"), unwrapping a single pointer.
func concreteCursorKey(info *types.Info, expr ast.Expr) (string, bool) {
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
