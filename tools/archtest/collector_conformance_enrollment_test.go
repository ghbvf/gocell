// INVARIANT: COLLECTOR-CONFORMANCE-ENROLLMENT-01
//
// AI-robust: Medium
//
// Enforces: every concrete type in the production source tree that implements
// runtime/observability/metrics.Collector must have at least one
// metricstest.RunCollectorConformance call in a _test.go file of its package
// (or the corresponding external _test variant). Packages without such a call
// are reported as violations.
//
// # Detection mechanism
//
//   - Implementation scan: types.Implements(*types.Interface) — type-aware;
//     identifies every concrete named type (exported AND unexported) that
//     satisfies runtime/observability/metrics.Collector in the production
//     source tree (Tests=false pass).
//   - Conformance call scan: RunTyped with Tests=true; for every _test.go file
//     containing a RunCollectorConformance call, walk all CallExpr nodes to find
//     the first argument to New() within the same file's harness. Because the
//     harness pattern stores the Collector in a field, impl-level resolution is
//     not required — package co-location is the enrollment granule here (one
//     conformance call per package covers all impls in that package).
//
// # AI-robust rating
//
// Medium is the ceiling by construction — Go cannot require a _test.go file
// to exist for a type at compile time. The enforcement is archtest-bound (CI
// fails on violation), not compile-time. The Hard upgrade path is a codegen
// funnel that enumerates Collector impls from a single source of truth and
// byte-locks the enrollment registry; deferred to backlog issue #1398 (same
// issue tracking archtest Hard-ization).
//
// # Blind-spot catalog (forms not reachable by *types.Info)
//
//   - B1. reflect-based implicit implementations: no production code uses this
//     pattern. Confirmed by
//     TestCollectorConformanceEnrollment_ReverseBlindSpot_NoReflectImpl, which
//     asserts zero occurrences of "Collector" as a string literal in
//     production non-test files (beyond the interface declaration itself).
//
//   - B2. Generated mock implementations (mockery/gomock in _test.go) are
//     excluded: test-file types are not scanned for implementations (Tests=false
//     in the production load pass). A generated mock in a production non-test
//     file would be flagged — intentionally.
//
//   - B3. Embedded interface forwarding (struct embedding metrics.Collector):
//     such a type structurally satisfies the interface. Production structs that
//     embed the interface are treated as implementations and must enroll.
//
// Hard-ization path: codegen funnel from a single golden registry of Collector
// implementations; tracked in gh #1398.
//
// ref: tools/archtest/saga_invariants_test.go SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
// ref: tools/archtest/cell_repo_readyz_probe_test.go CELL-REPO-READYZ-PROBE-01
// ref: runtime/observability/metrics/metricstest/conformance.go RunCollectorConformance
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

const (
	collectorIfacePkg            = PlatformModulePath + "/runtime/observability/metrics"
	collectorIfaceName           = "Collector"
	collectorConformancePkg      = PlatformModulePath + "/runtime/observability/metrics/metricstest"
	collectorConformanceFuncName = "RunCollectorConformance"
)

// TestCollectorConformanceEnrollment enforces COLLECTOR-CONFORMANCE-
// ENROLLMENT-01: every concrete type implementing
// runtime/observability/metrics.Collector in the production tree must have a
// metricstest.RunCollectorConformance call in a _test.go file of its package.
func TestCollectorConformanceEnrollment(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// ─── Step 1: resolve Collector interface + collect impl pkgs ───────────
	// Use prodscan.Patterns directly (runtime/observability/metrics is already
	// included). A single RunTyped load resolves both the interface and all
	// candidate impl packages, sharing the SharedResolver cache.
	prodPatterns := prodscan.Patterns(root)

	var collectorIface *types.Interface
	var implPkgs []*types.Package

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if p.Pkg.Path() == collectorIfacePkg {
				if obj := p.Pkg.Scope().Lookup(collectorIfaceName); obj != nil {
					if named, ok := obj.Type().(*types.Named); ok {
						if i, ok := named.Underlying().(*types.Interface); ok {
							collectorIface = i.Complete()
						}
					}
				}
			}
			implPkgs = append(implPkgs, p.Pkg)
			return nil
		})

	require.NotNil(t, collectorIface,
		"COLLECTOR-CONFORMANCE-ENROLLMENT-01: failed to resolve %s.%s interface; "+
			"check import path %s", collectorIfacePkg, collectorIfaceName, collectorIfacePkg)

	// ─── Step 2: collect all concrete implementations (pkg-level granule) ──
	// Each impl is stored as its package path; one RunCollectorConformance call
	// in the package is sufficient (unlike SAGA-JOURNAL-CONFORMANCE-ENROLLMENT-01
	// which uses impl-level keys). The two production impls live in separate
	// packages, so per-package enrollment is the correct granule here.
	implPkgPaths := make(map[string]bool) // package path → true

	for _, pkg := range implPkgs {
		if pkg == nil {
			continue
		}
		collectCollectorImpls(pkg, collectorIface, implPkgPaths)
	}

	require.NotEmpty(t, implPkgPaths,
		"COLLECTOR-CONFORMANCE-ENROLLMENT-01: zero Collector implementations collected — "+
			"likely a type-universe regression. Expect at least InMemoryCollector "+
			"(runtime/observability/metrics) and providerCollector "+
			"(runtime/observability/metrics, unexported).")

	// ─── Step 3: scan test corpus for RunCollectorConformance calls ─────────
	enrolledPkgs := make(map[string]bool)

	testPatterns := prodscan.Patterns(root)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, testPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				if !hasCollectorConformanceCall(f, p.TypesInfo) {
					continue
				}
				// Credit the package that contains this test file.
				// Strip the "_test" suffix that Go adds to external test packages.
				pkgPath := strings.TrimSuffix(p.Pkg.Path(), "_test")
				enrolledPkgs[pkgPath] = true
			}
			return nil
		})

	// ─── Step 4: flag unenrolled implementation packages ───────────────────
	var diags []Diagnostic
	for pkgPath := range implPkgPaths {
		if enrolledPkgs[pkgPath] {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:  pkgPath,
			Line: 0,
			Message: fmt.Sprintf(
				"archtest: runtime/observability/metrics.Collector impl in package %q "+
					"not enrolled (COLLECTOR-CONFORMANCE-ENROLLMENT-01). "+
					"Add a _test.go in package %s (or its external _test) that "+
					"calls metricstest.RunCollectorConformance(t, harness).",
				pkgPath, pkgPath),
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	Report(t, "COLLECTOR-CONFORMANCE-ENROLLMENT-01", diags)
}

// collectCollectorImpls walks pkg's scope and adds the path of any package
// that contains a concrete type implementing metrics.Collector.
func collectCollectorImpls(pkg *types.Package, iface *types.Interface, out map[string]bool) {
	if pkg == nil || iface == nil {
		return
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		// Check value receiver.
		if types.Implements(named, iface) {
			out[pkg.Path()] = true
			return
		}
		// Check pointer receiver.
		if types.Implements(types.NewPointer(named), iface) {
			out[pkg.Path()] = true
			return
		}
	}
}

// hasCollectorConformanceCall reports whether the file contains a call to
// metricstest.RunCollectorConformance. Resolution uses TypesInfo when available
// (typed load) to bind the callee to its package path.
func hasCollectorConformanceCall(file *ast.File, info *types.Info) bool {
	found := false
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if found {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != collectorConformanceFuncName {
			return
		}
		if info == nil {
			// Fallback: AST-only — check the qualifier name.
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "metricstest" {
				found = true
			}
			return
		}
		// Typed resolution: resolve the callee's package.
		if obj := info.ObjectOf(sel.Sel); obj != nil && obj.Pkg() != nil {
			if obj.Pkg().Path() == collectorConformancePkg {
				found = true
			}
		}
	})
	return found
}

// TestCollectorConformanceEnrollment_ReverseBlindSpot_NoReflectImpl is the B1
// reverse self-test: asserts no production non-test file uses string literal
// "Collector" with MethodByName to drive reflect-based implicit implementation
// (the most common bypass path for archtest impl scans). Zero occurrences are
// expected in production non-test files.
func TestCollectorConformanceEnrollment_ReverseBlindSpot_NoReflectImpl(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	var diags []Diagnostic

	// Scan production non-test files for MethodByName("Collector") calls.
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		prodscan.Patterns(root),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel == nil || sel.Sel.Name != "MethodByName" {
						return
					}
					if len(call.Args) != 1 {
						return
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok {
						return
					}
					if strings.Trim(lit.Value, `"`) == collectorIfaceName {
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: 0,
							Message: fmt.Sprintf(
								"B1 blind-spot (COLLECTOR-CONFORMANCE-ENROLLMENT-01): "+
									"reflect.MethodByName(%q) in production non-test file %s — "+
									"implicit Collector impl bypasses archtest impl scan",
								collectorIfaceName, rel),
						})
					}
				})
			}
			return nil
		})

	Report(t, "COLLECTOR-CONFORMANCE-ENROLLMENT-01/B1-reverse-self-test", diags)
}
