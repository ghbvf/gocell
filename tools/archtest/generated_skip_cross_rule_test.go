package archtest

// INVARIANT: GENERATED-SKIP-CROSS-RULE-INVARIANT-01
//
// Cross-rule invariant for archtest authors. When a test loads the real
// module root via typeseval.SharedResolver / typeseval.LoadPackages with the
// "./..." pattern, the loaded package set includes generated/ packages
// (anchored by TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3
// in outbox_invariants_test.go). The test then iterates pkg.Syntax and runs
// AST assertions, which would silently extend the rule's scope into codegen
// output unless the test explicitly skips generated/-rel paths via
// typeseval.IsGeneratedRelPath.
//
// This invariant fires when the same file:
//
//   1. Calls typeseval.SharedResolver(...) or typeseval.LoadPackages(...)
//      with the "./..." string literal in one of its positional args, AND
//   2. The call's first positional arg ultimately resolves to findModuleRoot
//      — either an inline findModuleRoot(...) call OR an Ident whose name
//      is bound by a FILE-LEVEL AssignStmt to findModuleRoot(...) anywhere
//      in the same file (so helpers like loadModule(t, root) are caught
//      via the caller's `root := findModuleRoot(t)` assignment), AND
//   3. The file does not call typeseval.IsGeneratedRelPath anywhere.
//
// Fixture-bound helpers (prod_duration_fixtures_test.go,
// prod_clock_injection_fixtures_test.go, exported_error_new_fixtures_test.go,
// test_time_literal_fixtures_test.go, the fixture-scan helpers in
// clock_invariants_test.go and goose_session_locker_fixtures_test.go) take
// fixtureDir as a parameter and the file only ever binds fixtureDir via
// filepath.Join(...), never findModuleRoot. Criterion 2 fails by AST shape,
// with no string allowlist. Tests that load a subtree pattern (e.g.
// "./tools/archtest/internal/wrapfixture/violation" for fixture detection)
// also escape via criterion 1: the "./..." literal is absent.
//
// AI-rebust: Medium+. Three-condition AST conjunction with no string-
// anchored allowlist. True Hard upgrade (typed-wrapped resolver baking in
// skip) touches 20+ callers and is tracked separately as
// LoadProductionPackagesAllPattern in the rollout plan §3.3 "Out of scope".
//
// ref: tools/archtest/archtest_verify_coverage_test.go (canonical cross-rule
// meta-archtest model)

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestGeneratedSkipCrossRuleInvariant01(t *testing.T) {
	t.Parallel()
	repoRoot := findModuleRoot(t)
	scope := scanner.DirsScope(repoRoot, []string{"tools/archtest"}, scanner.IncludeTests())

	var offenders []string
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Restrict to the top-level archtest package, matching the script's
		// ./tools/archtest non-recursive package selector (subpackages have
		// their own ./tools/archtest/internal/... test entry and do not load
		// production packages via SharedResolver).
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.Rel, "_test.go") {
			return
		}
		// Skip the meta-archtest itself so additions to the rule's body
		// (e.g. helper rename) do not trigger the invariant.
		if filepath.Base(fc.Rel) == "generated_skip_cross_rule_test.go" {
			return
		}
		if !fileLoadsRealRootAllPattern(fc.File) {
			return
		}
		if fileCallsIsGeneratedRelPath(fc.File) {
			return
		}
		offenders = append(offenders, filepath.ToSlash(fc.Rel))
	})

	sort.Strings(offenders)
	for _, rel := range offenders {
		t.Errorf("GENERATED-SKIP-CROSS-RULE-INVARIANT-01: %s loads the module root with \"./...\" "+
			"(typeseval.SharedResolver / typeseval.LoadPackages) but does not call "+
			"typeseval.IsGeneratedRelPath to skip codegen output before iterating pkg.Syntax. "+
			"Add `if typeseval.IsGeneratedRelPath(rel) { continue }` at the iteration site, "+
			"or restrict the load pattern to a subtree that excludes generated/.",
			rel)
	}
}

// fileLoadsRealRootAllPattern returns true when f contains a
// typeseval.SharedResolver / typeseval.LoadPackages call site whose
// positional args include the "./..." literal AND whose first arg resolves
// to findModuleRoot at the file level — either an inline findModuleRoot(...)
// call OR an Ident whose name is bound elsewhere in the same file by an
// AssignStmt whose RHS calls findModuleRoot. The file-level scope catches
// helper signatures like loadModule(t, root) where the param `root` is bound
// from findModuleRoot in caller test functions in the same file.
func fileLoadsRealRootAllPattern(f *ast.File) bool {
	var matched bool
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if matched {
			return
		}
		if !isSharedResolverOrLoadPackagesCall(call) {
			return
		}
		if len(call.Args) < 2 {
			return
		}
		if !callHasAllPatternLiteralArg(call) {
			return
		}
		if firstArgResolvesToFindModuleRoot(f, call.Args[0]) {
			matched = true
		}
	})
	return matched
}

// isSharedResolverOrLoadPackagesCall returns true when call's Fun is a
// SelectorExpr whose Sel name is SharedResolver or LoadPackages. The
// receiver package is not checked: shadowing typeseval.SharedResolver under
// the same symbol name in archtest test code would itself be the original
// foot-gun this invariant guards against.
func isSharedResolverOrLoadPackagesCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	switch sel.Sel.Name {
	case "SharedResolver", "LoadPackages":
		return true
	}
	return false
}

// callHasAllPatternLiteralArg returns true when call's direct children
// include the BasicLit "./..." (the broad pattern that includes generated/).
// Uses scanner.EachInChildren so the for-range + type assertion form banned
// by SCANNER-FRAMEWORK-USAGE-01 is structurally avoided.
func callHasAllPatternLiteralArg(call *ast.CallExpr) bool {
	var found bool
	scanner.EachInChildren[ast.BasicLit](call, func(lit *ast.BasicLit) {
		if !found && lit.Value == `"./..."` {
			found = true
		}
	})
	return found
}

// firstArgResolvesToFindModuleRoot returns true when arg ultimately
// represents the real repo root: an inline findModuleRoot(...) call OR an
// Ident bound by a file-level AssignStmt to findModuleRoot.
func firstArgResolvesToFindModuleRoot(f *ast.File, arg ast.Expr) bool {
	if exprIsCallToFindModuleRoot(arg) {
		return true
	}
	id := exprToIdent(arg)
	if id == nil {
		return false
	}
	return fileBindsIdentFromFindModuleRoot(f, id.Name)
}

// exprIsCallToFindModuleRoot returns true when e is a CallExpr whose Fun
// (or Fun.Sel for selector forms) is the identifier findModuleRoot.
func exprIsCallToFindModuleRoot(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "findModuleRoot"
	case *ast.SelectorExpr:
		return fun.Sel != nil && fun.Sel.Name == "findModuleRoot"
	}
	return false
}

// fileBindsIdentFromFindModuleRoot returns true when f contains any
// AssignStmt whose LHS contains an Ident named name AND whose RHS contains
// a call to findModuleRoot. File-level scope so a helper's parameter (e.g.
// `root` in loadModule(t, root)) is matched against its callers'
// `root := findModuleRoot(t)` assignments in the same file.
func fileBindsIdentFromFindModuleRoot(f *ast.File, name string) bool {
	var matched bool
	scanner.EachInSubtree[ast.AssignStmt](f, func(as *ast.AssignStmt) {
		if matched {
			return
		}
		if !assignLhsHasIdentNamed(as, name) {
			return
		}
		if !assignRhsCallsFindModuleRoot(as) {
			return
		}
		matched = true
	})
	return matched
}

// assignLhsHasIdentNamed returns true when as.Lhs contains an Ident whose
// Name equals name. The type assertion is funneled through exprToIdent
// (defined in security_defaults_test.go) so the for-range loop body has no
// inline type assertion — keeping the scan compliant with
// SCANNER-FRAMEWORK-USAGE-01 form (a) detection.
func assignLhsHasIdentNamed(as *ast.AssignStmt, name string) bool {
	for _, lhs := range as.Lhs {
		if id := exprToIdent(lhs); id != nil && id.Name == name {
			return true
		}
	}
	return false
}

// assignRhsCallsFindModuleRoot returns true when any expression in as.Rhs is
// a call to findModuleRoot (the only form callers use to discover the real
// repo go.mod ancestor in archtest tests).
func assignRhsCallsFindModuleRoot(as *ast.AssignStmt) bool {
	for _, rhs := range as.Rhs {
		if exprIsCallToFindModuleRoot(rhs) {
			return true
		}
	}
	return false
}

// fileCallsIsGeneratedRelPath returns true when f references the symbol
// typeseval.IsGeneratedRelPath anywhere — either as a SelectorExpr (the
// standard typeseval.IsGeneratedRelPath form) or as a bare Ident at a call
// position (dot-import form). Compliance is file-level: any call site is
// sufficient, mirroring outbox_invariants_test.go where the skip lives
// inside the iteration helper.
func fileCallsIsGeneratedRelPath(f *ast.File) bool {
	var found bool
	scanner.EachInSubtree[ast.SelectorExpr](f, func(sel *ast.SelectorExpr) {
		if !found && sel.Sel != nil && sel.Sel.Name == "IsGeneratedRelPath" {
			found = true
		}
	})
	if found {
		return true
	}
	scanner.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if found {
			return
		}
		if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "IsGeneratedRelPath" {
			found = true
		}
	})
	return found
}
