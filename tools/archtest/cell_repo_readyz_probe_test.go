// INVARIANT: CELL-REPO-READYZ-PROBE-01
//
// cell_repo_readyz_probe_test.go — CELL-REPO-READYZ-PROBE-01
//
// Rule: cells/ production code (non-test .go files) must register repo
// readiness probes exclusively through cell.RegisterRepoReadiness. Two banned
// forms are detected and one conformance backstop is enforced:
//
//   - N1 (Hard form lock — anonymous Health duck-type ban): *ast.TypeAssertExpr
//     in cells/** non-test files whose asserted type is an anonymous
//     *types.Interface exposing a method Health(context.Context) error.
//     Reverse self-check: named-interface assertions (e.g. x.(cell.HealthProber))
//     must NOT be flagged.
//
//   - N2 (Hard form lock — direct named reg.Health ban for repo): call to
//     method Health on kernel/cell.Registry (or a Registry-implementing interface)
//     where the first argument is a *ast.BasicLit string. The emitter drain
//     reg.Health(k, v) where the first arg is a range-variable Ident must NOT
//     be flagged.
//
//   - P1 (Medium backstop — conformance auto-join): every concrete type in
//     cells/ + adapters/ + runtime/ that implements kernel/cell.RepoHealthProber
//     must appear as an argument in at least one celltest.RunRepoReadinessConformance
//     call somewhere in the test source corpus.
//
// # Funnel 双向锁评级
//
//   - Downstream Hard (N1/N2 form lock): banned forms detected via type-aware
//     AST scan (RunTyped + *types.Info). The only two banned shapes are the
//     anonymous-interface duck-type assert (N1) and the bare reg.Health string-
//     literal call (N2). Any other shape compiles but fails CI immediately —
//     no gray zone.
//   - Upstream Hard: cell.RegisterRepoReadiness is the single typed funnel
//     (kernel/cell/repo_readiness.go). Its signature enforces cell.RepoHealthProber
//     at compile time. Using any other registration shape violates N1 or N2 and
//     fails this archtest. The funnel achieves "form uniqueness + archtest fail-
//     on-deviation" — equivalent grade to panic(panicregister.Approved(...)) per
//     ai-collab.md §Hard范本.
//   - P1 = Medium backstop: ensures every RepoHealthProber implementation is
//     wired into celltest.RunRepoReadinessConformance so the differentiated
//     failure-domain property is exercised.
//
// # Tool blind spots (forms RunTyped/TypesInfo cannot see)
//
// The following AST forms are outside *types.Info resolution and would bypass
// detection. Reverse self-check tests confirm they do NOT appear in current
// production AST:
//
//	B1. Reflection-based health registration: a call to reflect.Value.
//	    MethodByName("Health").Call(...) would bypass both N1 and N2.
//	    Reverse self-check: TestCellRepoReadyzProbe_ReverseBlindSpot_NoReflectHealth
//	    confirms no production cells/ file uses MethodByName("Health").
//
//	B2. Cross-package helper indirection (non-funnel): wrapping reg.Health in a
//	    non-RegisterRepoReadiness helper inside a cell package would hide the
//	    string literal from N2.
//	    Reverse self-check: TestCellRepoReadyzProbe_ReverseBlindSpot_NoLocalHealthWrapper
//	    confirms no cells/ non-test file defines a local function whose name
//	    contains "Health" AND whose body calls reg.Health with a string literal
//	    (excluding RegisterRepoReadiness itself).
//
// ref: kernel/cell/repo_readiness.go — RegisterRepoReadiness typed funnel
// ref: kernel/cell/celltest/repo_readiness_conformance.go — RunRepoReadinessConformance
// ref: .claude/rules/gocell/observability.md — Readyz Probe 命名 + Cell 级别 Repo Readiness Probe
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// repoReadyzCellPkgPath is the import path of kernel/cell.
const repoReadyzCellPkgPath = "github.com/ghbvf/gocell/kernel/cell"

// repoReadyzCelltestPkgPath is the import path of kernel/cell/celltest.
const repoReadyzCelltestPkgPath = "github.com/ghbvf/gocell/kernel/cell/celltest"

// repoReadyzConformanceFuncName is the name of the conformance harness.
const repoReadyzConformanceFuncName = "RunRepoReadinessConformance"

// INVARIANT: CELL-REPO-READYZ-PROBE-01
//
// TestCellRepoReadyzProbe enforces three sub-rules against the production tree:
//
//	N1 — anonymous Health duck-type ban in cells/ non-test files.
//	N2 — direct reg.Health(stringLiteral, fn) ban in cells/ non-test files.
//	P1 — every concrete RepoHealthProber implementation wired to conformance.
//
// See package-level godoc for full detection strategy, blind spots, and rating.
func TestCellRepoReadyzProbe(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	allPatterns := prodscan.PatternsExtended(root)
	prodPatterns := []string{"./cells/...", "./adapters/...", "./runtime/..."}
	testPatterns := prodscan.Patterns(root)

	// -----------------------------------------------------------------------
	// N1 + N2: scan cells/ production files for banned forms.
	// -----------------------------------------------------------------------
	var n1Diags, n2Diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, allPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			if !strings.HasPrefix(p.Pkg.Path(), "github.com/ghbvf/gocell/cells/") {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				n1Diags = append(n1Diags, scanRepoReadyzN1(p.Fset, f, rel, p.TypesInfo)...)
				n2Diags = append(n2Diags, scanRepoReadyzN2(p.Fset, f, rel, p.TypesInfo)...)
			}
			return nil
		})

	Report(t, "CELL-REPO-READYZ-PROBE-01/N1", n1Diags)
	Report(t, "CELL-REPO-READYZ-PROBE-01/N2", n2Diags)

	// -----------------------------------------------------------------------
	// P1: load RepoHealthProber interface.
	// -----------------------------------------------------------------------
	var repoHealthProberIface *types.Interface
	_ = RunTyped(t, TypedOpts{Tests: false}, []string{"./kernel/cell/..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil || p.Pkg.Path() != repoReadyzCellPkgPath {
			return nil
		}
		obj := p.Pkg.Scope().Lookup("RepoHealthProber")
		if obj == nil {
			return nil
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			return nil
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			return nil
		}
		repoHealthProberIface = iface.Complete()
		return nil
	})
	require.NotNil(t, repoHealthProberIface, "CELL-REPO-READYZ-PROBE-01/P1: failed to load RepoHealthProber")

	// P1: collect concrete implementations.
	implSet := make(map[string]bool)
	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, prodPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			collectRepoHealthProberImpls(p.Pkg, repoHealthProberIface, implSet)
			return nil
		})

	// P1: scan test corpus for conformance coverage.
	conformanceCovered := make(map[string]bool)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, testPatterns,
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				collectRepoReadyzConformanceCoverage(f, p.TypesInfo, conformanceCovered)
			}
			return nil
		})

	// P1: flag uncovered implementations.
	var p1Diags []Diagnostic
	for impl := range implSet {
		if !conformanceCovered[impl] {
			p1Diags = append(p1Diags, Diagnostic{
				Rel:  impl,
				Line: 0,
				Message: fmt.Sprintf(
					"%s implements kernel/cell.RepoHealthProber but has no "+
						"celltest.RunRepoReadinessConformance call in the test corpus "+
						"(CELL-REPO-READYZ-PROBE-01/P1)",
					impl),
			})
		}
	}
	sort.Slice(p1Diags, func(i, j int) bool { return p1Diags[i].Rel < p1Diags[j].Rel })
	Report(t, "CELL-REPO-READYZ-PROBE-01/P1", p1Diags)
}

// ─── N1 helpers ─────────────────────────────────────────────────────────────

// isAnonInterfaceWithHealthMethod reports whether t is an anonymous *types.Interface
// (not a *types.Named wrapping an interface) that exposes a method "Health" with
// signature func(context.Context) error.
//
// Named interfaces (e.g. cell.HealthProber resolved as *types.Named) are NOT
// flagged — the rule targets only inline anonymous duck-type assertions.
func isAnonInterfaceWithHealthMethod(t types.Type) bool {
	// Must be a bare *types.Interface, not a *types.Named.
	iface, ok := t.(*types.Interface)
	if !ok {
		return false
	}
	for i := 0; i < iface.NumMethods(); i++ {
		m := iface.Method(i)
		if m.Name() != "Health" {
			continue
		}
		sig, ok := m.Type().(*types.Signature)
		if !ok {
			continue
		}
		if sig.Params().Len() == 1 && sig.Results().Len() == 1 &&
			isContextType(sig.Params().At(0).Type()) &&
			isErrorType(sig.Results().At(0).Type()) {
			return true
		}
	}
	return false
}

// isContextType reports whether t is context.Context.
func isContextType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "context" && obj.Name() == "Context"
}

// isErrorType reports whether t is the builtin error interface.
func isErrorType(t types.Type) bool {
	iface, ok := t.Underlying().(*types.Interface)
	if !ok {
		return false
	}
	return iface.NumMethods() == 1 && iface.Method(0).Name() == "Error"
}

// scanRepoReadyzN1 walks file for anonymous Health duck-type assertions.
func scanRepoReadyzN1(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	if info == nil {
		return nil
	}
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.TypeAssertExpr](file, func(expr *ast.TypeAssertExpr) {
		if expr.Type == nil {
			return // type switch — skip
		}
		tv, ok := info.Types[expr.Type]
		if !ok {
			return
		}
		if !isAnonInterfaceWithHealthMethod(tv.Type) {
			return
		}
		line := fset.Position(expr.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "anonymous interface{ Health(context.Context) error } duck-type assertion; " +
					"use cell.RegisterRepoReadiness instead (CELL-REPO-READYZ-PROBE-01/N1)",
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── N2 helpers ─────────────────────────────────────────────────────────────

// isRegistryHealthCall reports whether call is a Health method invocation on a
// kernel/cell.Registry (or any receiver whose type contains that method),
// resolved via TypesInfo.Selections.
func isRegistryHealthCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Health" {
		return false
	}
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	return fn.Pkg() != nil && fn.Pkg().Path() == repoReadyzCellPkgPath && fn.Name() == "Health"
}

// scanRepoReadyzN2 walks file for reg.Health(stringLiteral, ...) calls.
func scanRepoReadyzN2(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	if info == nil {
		return nil
	}
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isRegistryHealthCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		// Flag only string BasicLit first arguments.
		// Range-variable Idents (emitter drain) are NOT flagged.
		if _, isLit := call.Args[0].(*ast.BasicLit); !isLit {
			return
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: `reg.Health("name", fn) direct call forbidden for repo probes; ` +
					"use cell.RegisterRepoReadiness(reg, name, p) (CELL-REPO-READYZ-PROBE-01/N2)",
			})
		}
	})

	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// ─── P1 helpers ─────────────────────────────────────────────────────────────

// collectRepoHealthProberImpls adds to implSet all exported concrete types in
// pkg that implement RepoHealthProber (directly or via pointer).
func collectRepoHealthProberImpls(pkg *types.Package, iface *types.Interface, implSet map[string]bool) {
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || !obj.Exported() {
			continue
		}
		t := obj.Type()
		if types.Implements(t, iface) || types.Implements(types.NewPointer(t), iface) {
			implSet[pkg.Path()+"."+name] = true
		}
	}
}

// collectRepoReadyzConformanceCoverage scans file for
// celltest.RunRepoReadinessConformance(t, name, healthy, broken) calls and
// records the pkg-path.TypeName of the healthy (3rd) and broken (4th)
// arguments.
func collectRepoReadyzConformanceCoverage(file *ast.File, info *types.Info, covered map[string]bool) {
	if info == nil {
		return
	}
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		pkgPath, name, ok := ResolvePackageRef(info, call.Fun)
		if !ok || pkgPath != repoReadyzCelltestPkgPath || name != repoReadyzConformanceFuncName {
			return
		}
		// Signature: RunRepoReadinessConformance(t, name, healthy, broken)
		// healthy = index 2, broken = index 3.
		if len(call.Args) < 3 {
			return
		}
		recordConformanceArgConcreteType(info, call.Args[2], covered)
		if len(call.Args) >= 4 {
			recordConformanceArgConcreteType(info, call.Args[3], covered)
		}
	})
}

// recordConformanceArgConcreteType extracts the underlying named type of expr
// (stripping pointer if present) and records it in covered.
func recordConformanceArgConcreteType(info *types.Info, expr ast.Expr, covered map[string]bool) {
	tv, ok := info.Types[expr]
	if !ok {
		return
	}
	t := tv.Type
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return
	}
	obj := named.Obj()
	if obj.Pkg() == nil {
		return
	}
	covered[obj.Pkg().Path()+"."+obj.Name()] = true
}

// ─── Fixture-based regression sub-tests ──────────────────────────────────────

// runRepoReadyzN1Fixture loads a fixture module and returns N1 diagnostics.
func runRepoReadyzN1Fixture(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	var out []Diagnostic
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				out = append(out, scanRepoReadyzN1(p.Fset, f, p.Rel(f), p.TypesInfo)...)
			}
			return nil
		})
	return out
}

// runRepoReadyzN2Fixture loads a fixture module and returns N2 diagnostics.
func runRepoReadyzN2Fixture(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	var out []Diagnostic
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				out = append(out, scanRepoReadyzN2(p.Fset, f, p.Rel(f), p.TypesInfo)...)
			}
			return nil
		})
	return out
}

// runRepoReadyzP1Fixture loads a fixture module as the "production" corpus,
// collects RepoHealthProber impls, and returns P1 violations (no test files →
// conformanceCovered is always empty → every impl is a violation).
//
// The RepoHealthProber interface must be resolved within the same RunTypedDir
// call that loads the fixture types. Loading it from the main module via a
// separate RunTyped call places it in a different type-checker universe; types
// from two distinct packages.Load invocations are never equal even when they
// share the same import path, so types.Implements would always return false.
// We extract the interface from p.Pkg.Imports() inside the single RunTypedDir
// pass so that the iface and the fixture types share one universe.
func runRepoReadyzP1Fixture(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()

	// iface is resolved lazily on the first Pass that imports kernel/cell.
	var iface *types.Interface
	implSet := make(map[string]bool)

	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		// Resolve the iface from the imports of this package if not yet found.
		// p.Pkg.Imports() returns the kernel/cell package loaded in the same
		// type universe as the fixture types, which makes types.Implements work.
		if iface == nil {
			for _, imp := range p.Pkg.Imports() {
				if imp.Path() != repoReadyzCellPkgPath {
					continue
				}
				obj := imp.Scope().Lookup("RepoHealthProber")
				if obj == nil {
					break
				}
				named, ok := obj.Type().(*types.Named)
				if !ok {
					break
				}
				underlying, ok := named.Underlying().(*types.Interface)
				if !ok {
					break
				}
				iface = underlying.Complete()
				break
			}
		}
		if iface != nil {
			collectRepoHealthProberImpls(p.Pkg, iface, implSet)
		}
		return nil
	})

	if iface == nil {
		t.Fatal("runRepoReadyzP1Fixture: could not resolve RepoHealthProber from fixture imports")
	}

	// No test corpus → no conformance coverage.
	var diags []Diagnostic
	for impl := range implSet {
		diags = append(diags, Diagnostic{
			Rel:     impl,
			Line:    0,
			Message: impl + " has no conformance wiring (CELL-REPO-READYZ-PROBE-01/P1)",
		})
	}
	sort.Slice(diags, func(i, j int) bool { return diags[i].Rel < diags[j].Rel })
	return diags
}

// TestCellRepoReadyzProbeFixtures runs RED/GREEN fixture cases for N1, N2, P1.
func TestCellRepoReadyzProbeFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "cell_repo_readyz_probe_fixtures")

	// N1 RED: anonymous Health duck-type assert must be caught.
	t.Run("N1_red_anon_health_duck_type", func(t *testing.T) {
		t.Parallel()
		got := runRepoReadyzN1Fixture(t, filepath.Join(base, "n1_anon_health_duck_type"))
		assert.Equal(t, 1, len(got),
			"N1 RED: expected 1 violation, got %d: %v", len(got), got)
	})

	// N2 RED: direct reg.Health with string literal must be caught.
	t.Run("N2_red_direct_reg_health_literal", func(t *testing.T) {
		t.Parallel()
		got := runRepoReadyzN2Fixture(t, filepath.Join(base, "n2_direct_reg_health_literal"))
		assert.Equal(t, 1, len(got),
			"N2 RED: expected 1 violation, got %d: %v", len(got), got)
	})

	// N3 GREEN: emitter drain reg.Health(k, v) with range vars and named
	// interface assertion must NOT be flagged.
	t.Run("N3_green_emitter_drain_range", func(t *testing.T) {
		t.Parallel()
		n1 := runRepoReadyzN1Fixture(t, filepath.Join(base, "n3_emitter_drain_range_ok"))
		n2 := runRepoReadyzN2Fixture(t, filepath.Join(base, "n3_emitter_drain_range_ok"))
		assert.Empty(t, n1, "N3 reverse: emitter drain must NOT trigger N1: %v", n1)
		assert.Empty(t, n2, "N3 reverse: emitter drain must NOT trigger N2: %v", n2)
	})

	// N4 GREEN: named-interface assertion x.(cell.HealthProber) must NOT
	// trigger N1.
	t.Run("N4_green_named_iface_assert", func(t *testing.T) {
		t.Parallel()
		got := runRepoReadyzN1Fixture(t, filepath.Join(base, "n4_named_iface_assert_ok"))
		assert.Empty(t, got,
			"N4 reverse: named-interface assertion must NOT trigger N1: %v", got)
	})

	// P1 RED: impl with no conformance wiring must be caught.
	t.Run("P1_red_impl_no_conformance", func(t *testing.T) {
		t.Parallel()
		got := runRepoReadyzP1Fixture(t, filepath.Join(base, "p1_impl_no_conformance"))
		assert.Equal(t, 1, len(got),
			"P1 RED: expected 1 violation (OrphanStore), got %d: %v", len(got), got)
	})
}

// ─── Blind-spot reverse self-checks ──────────────────────────────────────────

// TestCellRepoReadyzProbe_ReverseBlindSpot_NoReflectHealth (blind spot B1)
// asserts that no production cells/ file uses reflect MethodByName("Health").
func TestCellRepoReadyzProbe_ReverseBlindSpot_NoReflectHealth(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./cells/..."},
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
					if !ok || sel.Sel.Name != "MethodByName" || len(call.Args) != 1 {
						return
					}
					lit, ok := call.Args[0].(*ast.BasicLit)
					if !ok {
						return
					}
					if strings.Trim(lit.Value, `"`) == "Health" {
						line := p.Fset.Position(call.Pos()).Line
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: line,
							Message: "reflect MethodByName(\"Health\") in cells/ bypasses " +
								"CELL-REPO-READYZ-PROBE-01 N1/N2 (blind spot B1)",
						})
					}
				})
			}
			return nil
		})

	assert.Empty(t, diags, "blind spot B1 self-check failed")
}

// TestCellRepoReadyzProbe_ReverseBlindSpot_NoLocalHealthWrapper (blind spot B2)
// asserts no cells/ non-test file has a local function whose name contains
// "Health" (other than RegisterRepoReadiness) AND whose body calls reg.Health
// with a string-literal first argument.
func TestCellRepoReadyzProbe_ReverseBlindSpot_NoLocalHealthWrapper(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var diags []Diagnostic

	_ = RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()},
		[]string{"./cells/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil {
				return nil
			}
			for _, f := range p.Files {
				rel := p.Rel(f)
				if strings.HasSuffix(rel, "_test.go") {
					continue
				}
				EachInSubtree[ast.FuncDecl](f, func(fn *ast.FuncDecl) {
					if !strings.Contains(fn.Name.Name, "Health") {
						return
					}
					if fn.Name.Name == "RegisterRepoReadiness" {
						return
					}
					if fn.Body == nil {
						return
					}
					EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok || sel.Sel.Name != "Health" {
							return
						}
						if len(call.Args) == 0 {
							return
						}
						if _, isLit := call.Args[0].(*ast.BasicLit); !isLit {
							return
						}
						line := p.Fset.Position(call.Pos()).Line
						diags = append(diags, Diagnostic{
							Rel:  rel,
							Line: line,
							Message: fmt.Sprintf(
								"local function %q calls reg.Health(stringLiteral) — "+
									"use cell.RegisterRepoReadiness (blind spot B2, "+
									"CELL-REPO-READYZ-PROBE-01/N2)",
								fn.Name.Name),
						})
					})
				})
			}
			return nil
		})

	assert.Empty(t, diags, "blind spot B2 self-check failed")
}
