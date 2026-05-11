package archtest

// INVARIANT: PRODUCTION-LOADER-FUNNEL-01
//
// Archtest tests under tools/archtest/*_test.go (top-level package, excluding
// internal/ subpackages) MUST load real-repo "./..." packages through
// typeseval.LoadProductionPackages — the typed funnel that pre-filters
// <module>/generated/ packages at the package-set level — rather than the
// raw typeseval.SharedResolver / typeseval.LoadPackages with the "./..."
// literal pattern.
//
// Why a typed funnel (Hard) and not a file-level grep (Soft):
//
//   - File-level grep for "calling typeseval.IsGeneratedRelPath" only
//     proves the symbol appears in the AST, not that it controls the
//     pkg.Syntax iteration. AI can satisfy the grep with `if false {
//     typeseval.IsGeneratedRelPath("") }` dead code, or by extracting the
//     skip helper into a sibling file that the iteration loop never calls.
//     Charter §1 classifies this as "字符串约定 / 名字 convention" — Soft.
//
//   - The typed funnel makes the wrong shape unrepresentable: callers
//     iterating pkg.Syntax must reach for ProductionResolver.Production()
//     (which has generated/ pre-filtered) or ProductionResolver.All() (which
//     names the opt-in to codegen output at the call site). There is no
//     untyped []*packages.Package leaving the loader.
//
// The named allowlist below is reserved for the LOADER ANCHOR test —
// TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3 — which
// MUST call SharedResolver("./...") directly because its purpose is to
// prove that SharedResolver loads generated/ packages. Any other allowlist
// entry needs an ADR-level justification and an explicit comment block.

import (
	"go/ast"
	"go/parser"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// productionLoaderFunnelAllowlist names the (file::function) pairs that may
// call typeseval.SharedResolver / typeseval.LoadPackages with the "./..."
// literal pattern against the real repo root. Reserved for the loader
// behavior anchor test.
var productionLoaderFunnelAllowlist = map[string]string{
	"tools/archtest/outbox_invariants_test.go::" +
		"TestOutboxHandleResultFactoryPreferred_GeneratedLoadAnchor_Wave3": "anchor: proves SharedResolver(./...) " +
		"loads generated/ packages; must call raw API to validate funnel premise",
}

func TestProductionLoaderFunnel01(t *testing.T) {
	t.Parallel()
	repoRoot := findModuleRoot(t)
	scope := scanner.DirsScope(repoRoot, []string{"tools/archtest"}, scanner.IncludeTests())

	type violation struct {
		Key string
	}
	var violations []violation

	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.Rel, "_test.go") {
			return
		}
		rel := filepath.ToSlash(fc.Rel)
		f := fc.File
		scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
			if fd.Body == nil || fd.Name == nil {
				return
			}
			if !funcDeclCallsBannedRealRepoLoader(f, fd) {
				return
			}
			key := rel + "::" + fd.Name.Name
			if _, ok := productionLoaderFunnelAllowlist[key]; ok {
				return
			}
			violations = append(violations, violation{Key: key})
		})
	})

	sort.Slice(violations, func(i, j int) bool { return violations[i].Key < violations[j].Key })
	for _, v := range violations {
		t.Errorf("PRODUCTION-LOADER-FUNNEL-01: %s calls typeseval.SharedResolver / typeseval.LoadPackages "+
			"with the \"./...\" literal AND the first arg resolves to findModuleRoot (real repo root) — "+
			"use typeseval.LoadProductionPackages(modRoot, modulePath, tests, tags) and iterate "+
			"resolver.Production(). Anchor tests that legitimately need to load generated/ packages may be "+
			"added to productionLoaderFunnelAllowlist with an ADR-level justification.", v.Key)
	}
}

// funcDeclCallsBannedRealRepoLoader returns true when fd's body contains a
// call to typeseval.SharedResolver or typeseval.LoadPackages where:
//
//   - the call has the "./..." string literal in its CallExpr subtree, AND
//   - the call's first positional arg resolves to findModuleRoot at the
//     file level (either an inline findModuleRoot(...) call, or an Ident
//     whose name is bound by a file-level AssignStmt to findModuleRoot).
//
// Fixture-bound helpers like prod_duration_fixtures_test.go::runFixtureScan
// take `fixtureDir string` as a parameter and the file only ever binds
// fixtureDir via filepath.Join, never findModuleRoot — they are structurally
// excluded by the first-arg trace, not by a string allowlist.
//
// Note: this archtest BANS a specific call form (real-repo "./..." loader).
// Unlike a "must call typeseval.IsGeneratedRelPath somewhere" presence
// check, the detection here resolves a real AST fact — does this call exist
// or not — and cannot be bypassed by adding dead code or sibling-file
// helpers. The combination with the ProductionResolver typed funnel
// (LoadProductionPackages/Production()) makes the iteration path Hard:
// callers reach codegen output only by spelling out .All() at the call
// site, which is itself easy to grep and review.
func funcDeclCallsBannedRealRepoLoader(f *ast.File, fd *ast.FuncDecl) bool {
	var found bool
	scanner.EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
		if found {
			return
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		switch sel.Sel.Name {
		case "SharedResolver", "LoadPackages":
		default:
			return
		}
		recv := exprToIdent(sel.X)
		if recv == nil || recv.Name != "typeseval" {
			return
		}
		if len(call.Args) < 2 {
			return
		}
		if !callExprContainsAllPatternLiteral(call) {
			return
		}
		if !firstArgResolvesToFindModuleRoot(f, call.Args[0]) {
			return
		}
		found = true
	})
	return found
}

// firstArgResolvesToFindModuleRoot returns true when arg ultimately
// represents the real repo root within file f:
//
//   - an inline findModuleRoot(...) call, OR
//   - an Ident whose name is bound by a file-level AssignStmt to a
//     findModuleRoot call (covers same-func bindings and helper signatures
//     like loadModule(t, root) where the caller in the same file does
//     `root := findModuleRoot(t)`).
//
// The parameter-name fallback used in earlier drafts is intentionally
// omitted: matching by param name conflates `root` (real-repo) with
// `fixtureDir` (synthetic) — fixture helpers in this codebase use the
// fixtureDir name and load synthetic testdata modules, so detecting
// parameter presence would over-flag them.
func firstArgResolvesToFindModuleRoot(f *ast.File, arg ast.Expr) bool {
	if callExprFunIsFindModuleRoot(arg) {
		return true
	}
	id := exprToIdent(arg)
	if id == nil {
		return false
	}
	return fileBindsIdentFromFindModuleRoot(f, id.Name)
}

// callExprFunIsFindModuleRoot returns true when e is a CallExpr whose Fun
// (or Fun.Sel for selector forms) is the identifier findModuleRoot.
func callExprFunIsFindModuleRoot(e ast.Expr) bool {
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

// fileBindsIdentFromFindModuleRoot returns true when f contains an
// AssignStmt whose LHS contains an Ident named `name` AND whose RHS calls
// findModuleRoot. File-level scope so a helper's parameter (e.g. `root`
// in loadModule(t, root)) is matched against the caller's
// `root := findModuleRoot(t)` assignment in the same file.
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
// Name equals name. The type assertion is funneled through exprToIdent so
// the for-range loop body has no inline type assertion (compliant with
// SCANNER-FRAMEWORK-USAGE-01).
func assignLhsHasIdentNamed(as *ast.AssignStmt, name string) bool {
	for _, lhs := range as.Lhs {
		if id := exprToIdent(lhs); id != nil && id.Name == name {
			return true
		}
	}
	return false
}

// assignRhsCallsFindModuleRoot returns true when any expression in as.Rhs
// is a call to findModuleRoot.
func assignRhsCallsFindModuleRoot(as *ast.AssignStmt) bool {
	for _, rhs := range as.Rhs {
		if callExprFunIsFindModuleRoot(rhs) {
			return true
		}
	}
	return false
}

// callExprContainsAllPatternLiteral returns true when call's AST subtree
// contains a BasicLit equal to `"./..."`. EachInSubtree covers direct args
// AND nested composite-literal spreads (e.g. `[]string{"./..."}...`).
func callExprContainsAllPatternLiteral(call *ast.CallExpr) bool {
	var found bool
	scanner.EachInSubtree[ast.BasicLit](call, func(lit *ast.BasicLit) {
		if !found && lit.Value == `"./..."` {
			found = true
		}
	})
	return found
}
