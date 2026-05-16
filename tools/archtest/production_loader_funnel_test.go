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
	"path/filepath"
	"sort"
	"strings"
	"testing"
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
	scope := DirsScope(repoRoot, []string{"tools/archtest"}, IncludeTests())

	type violation struct {
		Key string
	}
	var violations []violation

	_ = Run(t, scope, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			rel := p.Rel(f)
			if filepath.ToSlash(filepath.Dir(rel)) != "tools/archtest" {
				continue
			}
			if !strings.HasSuffix(rel, "_test.go") {
				continue
			}
			EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
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
		}
		return nil
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
//   - the FuncDecl body contains a "./..." BasicLit somewhere
//     (catches direct args, composite-literal spreads, and same-function
//     variable indirection like `pat := "./..."; LoadPackages(root, _, _, pat)`),
//     AND
//   - the call's first positional arg resolves to findModuleRoot at the
//     file level (either an inline findModuleRoot(...) call, or an Ident
//     whose name is bound by a file-level AssignStmt to findModuleRoot).
//
// Fixture-bound helpers like prod_duration_fixtures_test.go::runFixtureScan
// take `fixtureDir string` as a parameter and the file only ever binds
// fixtureDir via filepath.Join, never findModuleRoot — they are structurally
// excluded by the first-arg trace, not by a string allowlist. They keep
// their own "./..." literal in fd.Body but escape via the first-arg trace.
//
// Function-body scope (not file scope) is intentional: clock_invariants_test.go
// holds real-repo loads in TestX functions and fixture loads in
// runXxxFixtureScan helpers in the SAME file. File-scope "./..." detection
// would over-flag the real-repo TestX functions whose pattern arg is a
// `patterns` variable. Function-scope keeps the two domains separate.
//
// Note: this archtest BANS a specific call form (real-repo "./..." loader).
// Unlike a "must call typeseval.IsGeneratedRelPath somewhere" presence
// check, the detection here resolves real AST facts — does the call exist
// AND does a `./...` literal exist in the SAME function body — and cannot
// be bypassed by adding dead code, sibling-file helpers, or same-function
// variable indirection. The combination with the ProductionResolver typed
// funnel (LoadProductionPackages/Production()) makes the iteration path
// Hard: callers reach codegen output only by spelling out .All() at the
// call site, which is itself easy to grep and review.
//
// Remaining marginal escape (documented Soft): cross-function `var pat =
// "./..."` at file scope referenced from fd.Body via Ident. Closing it
// requires unexporting typeseval.SharedResolver/LoadPackages and providing
// an explicit LoadPackagesForFixtures + internal anchor accessor; tracked
// as PRODUCTION-LOADER-API-PRIVATE-HARD-UPGRADE-01 in docs/backlog.md
// (trigger: first sighting of the cross-function indirection pattern in
// any archtest).
func funcDeclCallsBannedRealRepoLoader(f *ast.File, fd *ast.FuncDecl) bool {
	if !funcBodyHasAllPatternLiteral(fd.Body) {
		return false
	}
	var found bool
	EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
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
		if !firstArgResolvesToFindModuleRoot(f, call.Args[0]) {
			return
		}
		found = true
	})
	return found
}

// funcBodyHasAllPatternLiteral returns true when body's AST subtree
// contains a BasicLit equal to `"./..."`. Function-body scope catches
// direct args, composite-literal spreads, and same-function variable
// indirection (`pat := "./..."; ...`).
func funcBodyHasAllPatternLiteral(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	var found bool
	EachInSubtree[ast.BasicLit](body, func(lit *ast.BasicLit) {
		if !found && lit.Value == `"./..."` {
			found = true
		}
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
	EachInSubtree[ast.AssignStmt](f, func(as *ast.AssignStmt) {
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
