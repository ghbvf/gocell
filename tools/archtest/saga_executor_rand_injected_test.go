// INVARIANT: SAGA-EXECUTOR-RAND-INJECTED-01
//
// saga_executor_rand_injected_test.go — funnel guarding random-source injection
// inside runtime/saga/executor.
//
// # Rule
//
// In every production .go file under runtime/saga/executor/ (excluding _test.go),
// package-level global functions from math/rand and math/rand/v2 are forbidden.
// The only allowed calls to these packages are explicit source constructors:
// rand.New, rand.NewPCG, rand.NewChaCha8 (math/rand/v2) and rand.New,
// rand.NewSource (math/rand). All other package-level functions
// (rand.Int64N, rand.Intn, rand.Float64, rand.Int63n, rand.Seed,
// rand.Shuffle, rand.Read, etc.) are forbidden.
//
// # Rationale
//
// Package-level global random functions use a shared global source that cannot
// be seeded or replaced per-test. The executor must inject its random source
// (passed at construction via WithJitterSource or similar) so that retry jitter
// is deterministic in tests and reproducible in production. Using the global
// source violates the clock-injection discipline: correctness in tests depends
// on controlling the source of non-determinism.
//
// # Detection mechanism
//
// Typed AST (RunTypedProduction): scan every CallExpr whose callee is a
// *ast.SelectorExpr. Resolve the callee Ident via TypesInfo.ObjectOf to a
// *types.Func. If the function's package path is "math/rand" or "math/rand/v2"
// AND it has a nil receiver (package-level function) AND its name is NOT in the
// allowed constructor set, report a violation.
//
// This is the same pattern used by PROD-CLOCK-INJECTION-01 for time.Now:
// type-driven via info.ObjectOf, immune to import aliases and dot-imports.
//
// # Allowed constructor set (package-level functions NOT flagged)
//
//   - math/rand/v2: New, NewPCG, NewChaCha8
//   - math/rand (v1): New, NewSource
//
// # AI-robust rating
//
// Hard downstream: CallExpr resolution uses TypesInfo.ObjectOf (types.Func +
// pkg path check + nil-receiver check). The allowed set is keyed by exact
// (pkg path, func name) pair — import alias does not help, since ObjectOf
// resolves to the canonical package regardless of local alias.
//
// Medium upstream: archtest locks call sites within the scanned package. It is
// not a type-system constraint (no sealed interface). A helper package outside
// runtime/saga/executor/ that calls the global rand and whose result is used by
// the executor is NOT caught by this rule. That indirect path is documented as
// a known blind spot (B1) tracked in gh issue #1183.
//
// # Blind spots (reverse self-tests below)
//
// B1 — indirect: executor imports a helper package that itself calls
//
//	rand.Int64N. Not detected by this rule (scope is executor package only).
//	Mitigated: helper packages in runtime/saga/ are also scoped by this rule
//	if they reside in runtime/saga/executor/. Cross-package helpers outside
//	that scope require call-graph analysis — deferred.
//
// B2 — dot-import: `import . "math/rand/v2"; Int64N(10)` — the Ident is the
//
//	call function reference directly (no SelectorExpr). HANDLED: the scanner
//	also walks ast.Ident nodes via info.ObjectOf to catch dot-imports, same
//	as PROD-CLOCK-INJECTION-01. Reverse self-test: production tree asserts
//	no dot-import of math/rand or math/rand/v2 exists in runtime/saga/executor/.
//
// ref: tools/archtest/clock_invariants_test.go (PROD-CLOCK-INJECTION-01, sibling
//
//	pattern for time.Now injection discipline).
//
// ref: .claude/rules/gocell/ai-robust.md §"typed marker funnel for unbounded ops".
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	sagaRandInjectedRuleID = "SAGA-EXECUTOR-RAND-INJECTED-01"
	// executorPkgPrefix is the module-relative path prefix for the
	// runtime/saga/executor package (production .go files, not _test.go).
	executorPkgPrefix = "runtime/saga/executor/"
)

// randV1PkgPath and randV2PkgPath are the canonical import paths for the two
// math/rand package versions. Both are checked so v1 usage is also caught.
const (
	randV1PkgPath = "math/rand"
	randV2PkgPath = "math/rand/v2"
)

// randAllowedConstructors is the set of function names that ARE permitted as
// package-level calls (they construct a source, not use the global source).
// Keyed by (pkgPath, funcName).
var randAllowedConstructors = map[string]bool{
	randV2PkgPath + ".New":        true,
	randV2PkgPath + ".NewPCG":     true,
	randV2PkgPath + ".NewChaCha8": true,
	randV1PkgPath + ".New":        true,
	randV1PkgPath + ".NewSource":  true,
}

// isRandGlobalForbidden reports whether fn is a package-level function in
// math/rand or math/rand/v2 that is NOT in the allowed constructor set.
func isRandGlobalForbidden(fn *types.Func) bool {
	if fn == nil || fn.Pkg() == nil {
		return false
	}
	pkgPath := fn.Pkg().Path()
	if pkgPath != randV1PkgPath && pkgPath != randV2PkgPath {
		return false
	}
	// Must be a package-level function (nil receiver), not a method.
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	key := pkgPath + "." + fn.Name()
	return !randAllowedConstructors[key]
}

// scanRandGlobalCalls walks file's AST and returns diagnostics for every
// call to a forbidden package-level global rand function. Detection is
// type-driven via info.ObjectOf — covers both qualified form (rand.Int64N)
// and dot-import form (Int64N after `import . "math/rand/v2"`).
func scanRandGlobalCalls(p *Pass, file *ast.File) []Diagnostic {
	rel := p.Rel(file)
	info := p.TypesInfo
	var out []Diagnostic
	seen := map[string]bool{}

	record := func(node ast.Node, fnName, pkgPath string) {
		line := p.Fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s.%s", rel, line, pkgPath, fnName)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				sagaRandInjectedRuleID+": %s.%s — "+
					"must use an injected *rand.Rand source (rand.New(rand.NewPCG(...))) "+
					"instead of the package-level global; "+
					"global rand is not injectable and makes jitter non-deterministic in tests",
				pkgPath, fnName,
			),
		})
	}

	// Qualified form: rand.Int64N(...)
	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		fn, ok := info.ObjectOf(e.Sel).(*types.Func)
		if !ok || !isRandGlobalForbidden(fn) {
			return
		}
		record(e, fn.Name(), fn.Pkg().Path())
	})

	// Dot-import form: Int64N(...) — bare Ident, no SelectorExpr.
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		fn, ok := info.ObjectOf(e).(*types.Func)
		if !ok || !isRandGlobalForbidden(fn) {
			return
		}
		record(e, fn.Name(), fn.Pkg().Path())
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// isExecutorProductionFile reports whether rel is a production .go file
// under runtime/saga/executor/ (not _test.go).
func isExecutorProductionFile(rel string) bool {
	return strings.HasPrefix(rel, executorPkgPrefix) && !strings.HasSuffix(rel, "_test.go")
}

// sagaExecutorRandFixturePattern returns the (relDir, pattern) pair for the
// given fixture case under saga_executor_rand_injected_fixtures.
func sagaExecutorRandFixturePattern(fix string) (dir, pattern string) {
	const fixturesDir = "saga_executor_rand_injected_fixtures"
	return filepath.Join("tools", "archtest", "testdata", fixturesDir, fix),
		"./tools/archtest/testdata/" + fixturesDir + "/" + fix
}

// TestSagaExecutorRandInjected_A1_NoGlobalRandInExecutor asserts that no
// production file under runtime/saga/executor/ calls a package-level global
// function from math/rand or math/rand/v2 (other than the allowed constructors
// rand.New / rand.NewPCG / rand.NewChaCha8 / rand.NewSource). This ensures that
// jitter randomness is injected and deterministic in tests.
//
// Blind spots documented in file-level godoc:
//   - B1: cross-package helpers — not scanned (scope = executor/ only).
//   - B2: dot-import — handled via ast.Ident walk (reverse self-test below).
func TestSagaExecutorRandInjected_A1_NoGlobalRandInExecutor(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tags: FlatNonDefaultTags()}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if !isExecutorProductionFile(rel) {
				continue
			}
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return out
	})
	Report(t, sagaRandInjectedRuleID+"-A1", diags)
}

// TestSagaExecutorRandInjected_BlindSpot_B2_NoDotImportRandInExecutor is the
// reverse self-test for blind spot B2 (dot-import of math/rand). It asserts
// that no production executor file uses a dot-import of math/rand or
// math/rand/v2, which would create an unlabeled call-site not covered by the
// SelectorExpr branch alone (though scanRandGlobalCalls's Ident walk handles it).
// The Ident-walk coverage means B2 is de-facto caught; this test asserts the
// absence of the pattern so future authors know it's disallowed.
//
// AST-only (pure): scanning import specs for dot-import is sufficient without
// type loading.
func TestSagaExecutorRandInjected_BlindSpot_B2_NoDotImportRandInExecutor(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{"runtime/saga/executor"})
	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, file := range p.Files {
			rel := filepath.ToSlash(p.Rel(file))
			if strings.HasSuffix(rel, "_test.go") {
				continue
			}
			if !strings.HasPrefix(rel, executorPkgPrefix) {
				continue
			}
			for _, imp := range file.Imports {
				if imp.Name == nil || imp.Name.Name != "." {
					continue
				}
				path := ""
				if imp.Path != nil {
					path = imp.Path.Value
				}
				if path == `"`+randV1PkgPath+`"` || path == `"`+randV2PkgPath+`"` {
					pos := p.Fset.Position(imp.Pos())
					out = append(out, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: sagaRandInjectedRuleID + "-B2 (blind-spot guard): " +
							"dot-import of math/rand or math/rand/v2 found in executor; " +
							"use qualified form `rand.Int64N(...)` (which is still forbidden) " +
							"or the injected source instead",
					})
				}
			}
		}
		return out
	})
	Report(t, sagaRandInjectedRuleID+"-B2", diags)
}

// TestSagaExecutorRandInjected_Detector_RedGlobalRandFixture loads the
// red_global_rand fixture and asserts the A1 detector fires with the expected
// diagnostic. This is the detector self-test: if A1's detection logic is
// broken, this test fails even though the production scan yields 0 diagnostics.
func TestSagaExecutorRandInjected_Detector_RedGlobalRandFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaExecutorRandFixturePattern("red_global_rand")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		// No scope filter for fixtures: scan all loaded files.
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}

// TestSagaExecutorRandInjected_Detector_RedDotImportRandFixture loads the
// red_dot_import_rand fixture (dot-imported `Int64N(...)` as a bare Ident) and
// asserts the A1 detector fires via its ast.Ident walk branch. Closes the B2
// fixture gap: prior to this fixture the dot-import branch had only the reverse
// self-test (asserting absence in production), not a positive detector proof.
func TestSagaExecutorRandInjected_Detector_RedDotImportRandFixture(t *testing.T) {
	root := findModuleRoot(t)
	relDir, pattern := sagaExecutorRandFixturePattern("red_dot_import_rand")
	diags := RunTypedFixture(t, FixtureOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil {
			return nil
		}
		var out []Diagnostic
		for _, file := range p.Files {
			out = append(out, scanRandGlobalCalls(p, file)...)
		}
		return out
	})
	AssertGolden(t, filepath.Join(root, relDir, "diag.golden"), diags)
}
