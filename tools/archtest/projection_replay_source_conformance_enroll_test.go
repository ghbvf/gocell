// INVARIANT: PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01
//
// PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01 — ReplaySource conformance backstop.
//
// Every concrete production type implementing kernel/projection.ReplaySource
// must appear as an argument to a projectiontest.RunReplaySourceConformance(...)
// call in a _test.go file somewhere in the production test corpus. Enrollment is
// credited by the impl's CONCRETE TYPE (the resolved type of the call's src
// argument), not by the enrolling test's package — consistent with the sibling
// PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01.
//
// This enforces the contract-fanout.md §5 M4 rule: every ReplaySource impl must be
// verified by the shared conformance harness. Adding a new impl without enrolling
// it is a CI failure.
//
// PR-03 status: genuinely-green. MemReplaySource is enrolled in
// kernel/projection/memstore_test.go::TestMemReplaySource_Conformance.
//
// # AI-robust grading
//
//   - Medium (typed impl-discovery + conformance call scan, both via *types.Info).
//     Same ceiling as PROJECTION-CHECKPOINT-CONFORMANCE-ENROLL-01: Go cannot
//     require a _test.go file to exist for a type at compile time. The behavioral
//     correctness of RunReplaySourceConformance itself is a separate guarantee.
//   - Negative coverage: the Medium guard's detection path is exercised against a
//     real fixture by TestProjectionReplaySourceConformanceEnroll01_RedFixture.
//
// # Blind spots (forms *types.Info cannot see)
//
//   - B1. reflect-based implicit implementations: no production projection code
//     uses this pattern. Confirmed by
//     TestProjectionReplaySourceConformanceEnroll01_ReverseBlindSpot_NoReflectImpl.
//   - B2. Generated mock implementations in _test.go: test-file types are not
//     scanned for implementations (Tests=false in the production load pass).
//   - B3. Indirect construction (factory returning an interface): the scanner
//     resolves the concrete type of the argument passed to RunReplaySourceConformance.
//     If the argument is an interface-typed variable, enrollment is not credited.
//
// ref: tools/archtest/projection_checkpoint_conformance_enroll_test.go (same pattern)
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
	replaySourceConformancePkg      = PlatformModulePath + "/kernel/projection/projectiontest"
	replaySourceConformanceFuncName = "RunReplaySourceConformance"

	replaySourceIfacePkg  = PlatformModulePath + "/kernel/projection"
	replaySourceIfaceName = "ReplaySource"

	fixtureReplayEnrollPkg = PlatformModulePath + "/tools/archtest/internal/projectionreplayenrollfixture"
)

// TestProjectionReplaySourceConformanceEnroll01 enforces PROJECTION-REPLAY-SOURCE-
// CONFORMANCE-ENROLL-01: every concrete production type implementing
// kernel/projection.ReplaySource must be enrolled in a
// projectiontest.RunReplaySourceConformance call in a _test.go file.
//
// Per-impl enrollment: the scanner resolves the concrete src type passed as
// the first non-t argument to RunReplaySourceConformance. A package with N impls
// must individually enroll each.
func TestProjectionReplaySourceConformanceEnroll01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	prodPatterns := prodscan.Patterns(root)

	// ─── Step 1: resolve ReplaySource interface + collect impl packages ──────
	var rsIface *types.Interface
	var rsImplPkgs []*types.Package

	_ = Run(t, Typed(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == replaySourceIfacePkg {
				if obj := p.Pkg.Scope().Lookup(replaySourceIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							rsIface = iface.Complete()
						}
					}
				}
			}
			rsImplPkgs = append(rsImplPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, rsIface,
		"PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01: failed to resolve ReplaySource interface; "+
			"check import path %s", replaySourceIfacePkg)

	// ─── Step 2: collect all concrete implementations ────────────────────────
	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range rsImplPkgs {
		if pkg == nil {
			continue
		}
		collectReplaySourceImplsForEnrollment(pkg, rsIface, implSet, implPkgSet)
	}

	require.NotEmpty(t, implSet,
		"PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01: zero ReplaySource implementations collected — "+
			"sanity anchor: at least kernel/projection.MemReplaySource must be found.")

	// ─── Step 3: scan test corpus for RunReplaySourceConformance callsites ────
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
				if !hasReplaySourceConformanceCall(f, p.TypesInfo) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != replaySourceConformancePkg || name != replaySourceConformanceFuncName {
						return
					}

					if len(call.Args) < 2 {
						return
					}
					if key, ok := concreteReplaySourceKey(p.TypesInfo, call.Args[1]); ok {
						enrolledImpls[key] = true
					}
				})
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementations ─────────────────────────────
	Report(t, "PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01",
		unenrolledReplaySourceDiags(implSet, enrolledImpls))
}

// unenrolledReplaySourceDiags returns one Diagnostic per ReplaySource impl in
// implSet that is absent from enrolledImpls. Shared by both the production
// test and the RED fixture test.
func unenrolledReplaySourceDiags(implSet, enrolledImpls map[string]bool) []Diagnostic {
	var diags []Diagnostic
	for implKey := range implSet {
		if enrolledImpls[implKey] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  implKey,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: projection.ReplaySource impl %q not enrolled in "+
					"projectiontest.RunReplaySourceConformance "+
					"(PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01). Add a _test.go that "+
					"calls projectiontest.RunReplaySourceConformance(t, <src>, seedFn) passing a "+
					"concretely-typed instance of this impl.",
				implKey,
			),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// TestProjectionReplaySourceConformanceEnroll01_RedFixture runs the REAL scanner
// over the synthetic fixture (internal/projectionreplayenrollfixture) to verify:
//   - enrolledReplaySource is NOT flagged (enrolled via RunReplaySourceConformance)
//   - unenrolledReplaySource IS flagged (no enrollment call)
func TestProjectionReplaySourceConformanceEnroll01_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	loadPatterns := []string{
		"./kernel/projection/...",
		"./tools/archtest/internal/projectionreplayenrollfixture/...",
	}

	// ─── Pass 1 (Tests:false): resolve iface + collect impls ─────────────────
	var rsIface *types.Interface
	var rsImplPkgs []*types.Package
	_ = Run(t, Fixture(FixtureOpts{Tests: false}, loadPatterns),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == replaySourceIfacePkg {
				if obj := p.Pkg.Scope().Lookup(replaySourceIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if iface, ok := named.Underlying().(*types.Interface); ok {
							rsIface = iface.Complete()
						}
					}
				}
			}
			rsImplPkgs = append(rsImplPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, rsIface, "RedFixture: could not resolve ReplaySource interface")

	implSet := make(map[string]bool)
	implPkgSet := make(map[string]bool)
	for _, pkg := range rsImplPkgs {
		if pkg != nil {
			collectReplaySourceImplsForEnrollment(pkg, rsIface, implSet, implPkgSet)
		}
	}

	// Restrict to fixture impls only.
	for k := range implSet {
		if !strings.HasPrefix(k, fixtureReplayEnrollPkg+".") {
			delete(implSet, k)
		}
	}
	unenrolledKey := fixtureReplayEnrollPkg + ".unenrolledReplaySource"
	enrolledKey := fixtureReplayEnrollPkg + ".enrolledReplaySource"
	require.Contains(t, implSet, unenrolledKey, "RedFixture: impl discovery must find unenrolledReplaySource")
	require.Contains(t, implSet, enrolledKey, "RedFixture: impl discovery must find enrolledReplaySource")

	// ─── Pass 2 (Tests:true): scan fixture _test.go for enrollment ───────────
	enrolledImpls := make(map[string]bool)
	_ = Run(t, Fixture(FixtureOpts{Tests: true},
		[]string{"./tools/archtest/internal/projectionreplayenrollfixture/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !hasReplaySourceConformanceCall(f, p.TypesInfo) {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					pkgPath, name, ok := ResolvePackageRef(p.TypesInfo, call.Fun)
					if !ok || pkgPath != replaySourceConformancePkg || name != replaySourceConformanceFuncName {
						return
					}
					if len(call.Args) < 2 {
						return
					}
					if key, ok := concreteReplaySourceKey(p.TypesInfo, call.Args[1]); ok {
						enrolledImpls[key] = true
					}
				})
			}
			return nil
		})

	require.True(t, enrolledImpls[enrolledKey],
		"RedFixture: the enrollment scan must credit enrolledReplaySource via its "+
			"RunReplaySourceConformance call (positive direction)")

	diags := unenrolledReplaySourceDiags(implSet, enrolledImpls)

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
		"RedFixture: the real scanner must FLAG unenrolledReplaySource (negative direction)")
	assert.False(t, flaggedEnrolled,
		"RedFixture: the real scanner must NOT flag enrolledReplaySource")
}

// TestProjectionReplaySourceConformanceEnroll01_ReverseBlindSpot_NoReflectImpl (B1)
// confirms no production non-test file uses the string literal "ReplaySource"
// as a reflect target.
func TestProjectionReplaySourceConformanceEnroll01_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
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
				if !ok || val != replaySourceIfaceName {
					return
				}
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: p.Fset.Position(lit.Pos()).Line,
					Message: "blind-spot B1: string literal \"ReplaySource\" in production code " +
						"outside kernel/projection may indicate reflect-based impl " +
						"(PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01)",
				})
			})
		}
		return out
	})

	assert.Empty(t, diags,
		"B1 reverse: no production non-test file outside kernel/projection should contain "+
			"the string literal %q as reflect bait", replaySourceIfaceName)
}

// ─── helpers ──────────────────────────────────────────────────────────────────

// collectReplaySourceImplsForEnrollment adds to implSet all concrete named types
// in pkg that implement ReplaySource (value or pointer receiver). Interface types
// and embedded-interface delegation wrappers are excluded.
func collectReplaySourceImplsForEnrollment(pkg *types.Package, iface *types.Interface, implSet, implPkgSet map[string]bool) {
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
			if isEmbedReplaySourceWrapper(t, iface) {
				continue
			}
			key := pkg.Path() + "." + name
			implSet[key] = true
			implPkgSet[pkg.Path()] = true
		}
	}
}

// isEmbedReplaySourceWrapper reports whether t is a struct that embeds the
// ReplaySource interface as an anonymous field (delegation wrapper).
func isEmbedReplaySourceWrapper(t types.Type, iface *types.Interface) bool {
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

// hasReplaySourceConformanceCall reports whether file contains at least one call
// to projectiontest.RunReplaySourceConformance.
func hasReplaySourceConformanceCall(file *ast.File, info *types.Info) bool {
	if info == nil {
		return false
	}
	_, ok := FindFirstInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) bool {
		pkgPath, name, resolved := ResolvePackageRef(info, call.Fun)
		return resolved && pkgPath == replaySourceConformancePkg && name == replaySourceConformanceFuncName
	})
	return ok
}

// concreteReplaySourceKey resolves expr's static type to a concrete impl key
// ("pkg/path.TypeName"), unwrapping a single pointer.
func concreteReplaySourceKey(info *types.Info, expr ast.Expr) (string, bool) {
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
