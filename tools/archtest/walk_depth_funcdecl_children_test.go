//go:build archtest

// INVARIANT: WALK-DEPTH-API-EACHCHILDREN-01
//
// WALK-DEPTH-API-EACHCHILDREN-01: a *ast.FuncDecl is ALWAYS a direct child of
// *ast.File — Go forbids nested function declarations (a function nested in a
// body is an *ast.FuncLit, not a FuncDecl). So instantiating the recursive
// subtree-axis walk helpers with ast.FuncDecl visits exactly the same nodes as
// the depth-1 axis while paying a pointless recursive descent, and — more to
// the point — encodes the WRONG depth semantics in the API name. The depth
// choice is a typed function-name selection (ai-robust.md "Hard 范本" #1
// "typed function choice for walk depth"), so the fix is the API NAME, not a
// runtime guard: use EachInChildren[ast.FuncDecl].
//
// This rule bans the recursive *Each*-subtree axis instantiated with
// ast.FuncDecl:
//   - EachInSubtree[ast.FuncDecl]        → EachInChildren[ast.FuncDecl]
//   - EachInSubtreeStopAt[ast.FuncDecl]  → EachInChildren[ast.FuncDecl]
//     (the stopAt boundary is moot at depth-1; banned to close the
//     trivial swap-the-variant bypass).
//
// Both the unqualified archtest façade (EachInSubtree, package
// github.com/ghbvf/gocell/tools/archtest) and the scanner-qualified form
// (scanner.EachInSubtree) are caught: the callee is resolved via
// info.Uses[ident] → *types.Func → Pkg().Path() ∈ {archtest, scanner} &&
// Name() ∈ banned-set, and the type argument via info.Uses[sel] →
// *types.TypeName → Pkg().Path()=="go/ast" && Name()=="FuncDecl". A same-name
// local func, an import alias, or a go/ast alias cannot disguise either side.
//
// AI-robust: Medium (CI-time type-aware AST scan). It is NOT Hard, and the
// ceiling is a genuine Go language limit, not a missing-effort gap: Hard would
// require making EachInSubtree[ast.FuncDecl] fail to COMPILE, but the helper's
// constraint is `interface{ *S; ast.Node }` and Go generics cannot express
// "any ast.Node EXCEPT FuncDecl" (a constraint cannot negate a concrete type),
// so EachInSubtree[ast.FuncDecl] is forever a legal instantiation. The
// typed-function-choice Hard portion is already carried by EachInSubtree vs
// EachInChildren being two distinct function names; the residual "for
// ast.FuncDecl you MUST pick children" step is only machine-checkable, hence
// Medium. Same class as the documented Go-language ceilings #1352 / #1452. No
// low-cost Hard path exists → no Hard-ization issue is filed; this paragraph
// is the recorded blind spot.
//
// Blind-spot inventory (each has a fixture or an honest scope declaration —
// ai-robust.md §"工具选定后强制盲区自检"):
//   - EachInSubtree[ast.FuncDecl] — red_eachinsubtree_funcdecl fixture.
//   - EachInSubtreeStopAt[ast.FuncDecl] — red_eachinsubtreestopat_funcdecl
//     fixture (0 production sites today; banned as anti-bypass).
//   - GREEN depth-1 EachInChildren[ast.FuncDecl] — green_eachinchildren_funcdecl.
//   - GREEN legitimate subtree walk of a genuinely-nesting node
//     (EachInSubtree[ast.CallExpr]) — green_eachinsubtree_callexpr (anti-overreach).
//   - Find-axis (FindFirstInSubtree[ast.FuncDecl]) — OUT OF SCOPE. The
//     find-first funnel is a separate concern (SCANNER-FRAMEWORK-USAGE-02 uses
//     FindFirstInSubtree as a sanctioned GREEN baseline), so the depth question
//     on that axis is deliberately not folded in here. Honest scope declaration.
//   - internal/scanner walker self-tests (eachnode_test.go) and internal
//     fixture packages legitimately instantiate EachInSubtree[ast.FuncDecl] to
//     exercise the walker / seed fixtures — exempt by path-scope: the live
//     dogfood scans only the top-level tools/archtest rule dir (depth-1 dir
//     filter), never internal/ subpackages. Path-scope, not an allowlist.
//   - string / comment textual mentions — not generic instantiations; out of
//     scope by construction (the scan resolves *ast.IndexExpr type args).
//
// CI coverage: the heavy production dogfood (TestWalkDepthFuncDeclChildren01,
// a ./tools/archtest/... typed load) runs nightly via the full archtest
// 24-shard matrix (auto-discovered by the //go:build archtest tag, no
// registration). The light reverse self-checks (TestWalkDepthFuncDeclChildren01_Fixtures
// + _AntiVacuity) ALSO run PR-time via hack/verify-archtest-invariants.sh's
// -run selector, mirroring the sibling funnel rules (REPLAYDEPS / SAGA-PROJECTION-DEPS
// / CONTRACT-OWNER-CELL), so a detector regression is caught at PR-time.
package archtest

import (
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleWalkDepthFuncDeclChildren = "WALK-DEPTH-API-EACHCHILDREN-01"
	walkDepthArchtestPkgPath      = PlatformModulePath + "/tools/archtest"
	walkDepthScannerPkgPath       = PlatformModulePath + "/tools/archtest/internal/scanner"
	walkDepthGoAstPkgPath         = "go/ast"
	walkDepthFuncDeclTypeName     = "FuncDecl"
	// walkDepthRuleDir is the single rule directory the live dogfood scans.
	// internal/ subpackages (scanner walker self-tests, fixture packages) are
	// excluded by this depth-1 dir filter — path-scope, not an allowlist.
	walkDepthRuleDir = "tools/archtest"
	// walkDepthMinScanned guards against a silently-shrunken scan (scope drift /
	// build-tag drop). The top-level tools/archtest dir holds ~400+ loaded
	// files (default + archtest-tagged), so a floor of 200 still tolerates
	// normal file churn while catching a >50% scope collapse — far tighter than
	// merely guarding the vacuous-zero case.
	walkDepthMinScanned = 200
)

// walkDepthBannedSubtreeFuncs maps each recursive subtree-axis Each* walk
// helper that must NOT be instantiated with ast.FuncDecl → its depth-1
// replacement. Kept non-empty + every entry exercised by a fixture (see the
// anti-vacuity subtest).
var walkDepthBannedSubtreeFuncs = map[string]string{
	"EachInSubtree":       "EachInChildren",
	"EachInSubtreeStopAt": "EachInChildren",
}

// collectWalkDepthFuncDeclViolations reports every generic instantiation of a
// banned subtree-axis walk helper with an ast.FuncDecl type argument in file.
// Walking *ast.IndexExpr / *ast.IndexListExpr (not just call-position) also
// catches the func-value form `f := EachInSubtree[ast.FuncDecl]`.
func collectWalkDepthFuncDeclViolations(info *types.Info, fset *token.FileSet, file *ast.File, rel string) []Diagnostic {
	if info == nil || fset == nil {
		return nil
	}
	var diags []Diagnostic
	add := func(fnExpr, sTypeArg ast.Expr) {
		if d, ok := walkDepthInstantiationViolation(info, fset, fnExpr, sTypeArg, rel); ok {
			diags = append(diags, d)
		}
	}
	// EachInSubtree[S, N] iterates nodes of element type S (N is its *S pointer,
	// inferred), so only the FIRST type argument decides the iterated node type.
	// IndexExpr = single explicit arg (the idiomatic EachInSubtree[ast.FuncDecl]);
	// IndexListExpr = 2+ explicit args, S = Indices[0]. Indexing the single S arg
	// (not for-ranging the []ast.Expr) keeps SCANNER-FRAMEWORK-USAGE-01 satisfied.
	EachInSubtree[ast.IndexExpr](file, func(ix *ast.IndexExpr) {
		add(ix.X, ix.Index)
	})
	EachInSubtree[ast.IndexListExpr](file, func(ix *ast.IndexListExpr) {
		if len(ix.Indices) > 0 {
			add(ix.X, ix.Indices[0])
		}
	})
	return diags
}

// walkDepthInstantiationViolation is the pure per-instantiation gate. sTypeArg
// is the first (element) type argument of the generic instantiation.
func walkDepthInstantiationViolation(
	info *types.Info, fset *token.FileSet, fnExpr, sTypeArg ast.Expr, rel string,
) (Diagnostic, bool) {
	base := walkDepthBaseIdent(fnExpr)
	if base == nil {
		return Diagnostic{}, false
	}
	fn, ok := info.Uses[base].(*types.Func)
	if !ok || fn.Pkg() == nil {
		return Diagnostic{}, false
	}
	if !walkDepthIsBannedSubtreeFunc(fn) {
		return Diagnostic{}, false
	}
	if !walkDepthTypeArgIsASTFuncDecl(info, sTypeArg) {
		return Diagnostic{}, false
	}
	return Diagnostic{
		Rel:  rel,
		Line: fset.Position(base.Pos()).Line,
		Message: ruleWalkDepthFuncDeclChildren + ": " + fn.Name() +
			"[ast.FuncDecl] — ast.FuncDecl is always a direct *ast.File child" +
			" (depth=1; Go forbids nested func declarations); use" +
			" EachInChildren[ast.FuncDecl] instead",
	}, true
}

// walkDepthBaseIdent returns the function-name ident of a (possibly qualified)
// generic-instantiation base expression: Ident for the unqualified façade
// (EachInSubtree) or SelectorExpr.Sel for the scanner-qualified form.
func walkDepthBaseIdent(e ast.Expr) *ast.Ident {
	switch x := e.(type) {
	case *ast.Ident:
		return x
	case *ast.SelectorExpr:
		return x.Sel
	default:
		return nil
	}
}

// walkDepthIsBannedSubtreeFunc reports whether fn is one of the banned
// subtree-axis walk helpers, defined in either the archtest façade or the
// scanner package.
func walkDepthIsBannedSubtreeFunc(fn *types.Func) bool {
	if path := fn.Pkg().Path(); path != walkDepthArchtestPkgPath && path != walkDepthScannerPkgPath {
		return false
	}
	_, banned := walkDepthBannedSubtreeFuncs[fn.Name()]
	return banned
}

// walkDepthTypeArgIsASTFuncDecl reports whether the element type argument
// resolves to go/ast.FuncDecl (type-resolved, alias-proof). Takes a single
// expr (not a slice) so it never for-ranges over []ast.Expr — see
// SCANNER-FRAMEWORK-USAGE-01.
func walkDepthTypeArgIsASTFuncDecl(info *types.Info, sTypeArg ast.Expr) bool {
	sel, ok := sTypeArg.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	tn, ok := info.Uses[sel.Sel].(*types.TypeName)
	if !ok || tn.Pkg() == nil {
		return false
	}
	return tn.Pkg().Path() == walkDepthGoAstPkgPath && tn.Name() == walkDepthFuncDeclTypeName
}

// TestWalkDepthFuncDeclChildren01 is the production dogfood: it scans every
// rule file in the top-level tools/archtest dir (both *.go and *_test.go, with
// the archtest leaf build tag active) and asserts ZERO banned
// EachInSubtree[ast.FuncDecl] / EachInSubtreeStopAt[ast.FuncDecl] sites remain.
// The dir filter excludes internal/ subpackages (walker self-tests + fixture
// packages legitimately exercise the recursive axis) by path-scope.
func TestWalkDepthFuncDeclChildren01(t *testing.T) {
	t.Parallel()

	var violations []Diagnostic
	scanned := 0
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: []string{archtestLeafBuildTag}}, []string{"./tools/archtest/..."}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				if filepath.ToSlash(filepath.Dir(rel)) != walkDepthRuleDir {
					continue
				}
				scanned++
				violations = append(violations, collectWalkDepthFuncDeclViolations(p.TypesInfo, p.Fset, f, rel)...)
			}
			return nil
		})

	require.GreaterOrEqual(t, scanned, walkDepthMinScanned,
		"%s scope regression: scanned only %d files in %s — the load went near-empty "+
			"(scope drift / archtest build tag dropped). Expected >= %d.",
		ruleWalkDepthFuncDeclChildren, scanned, walkDepthRuleDir, walkDepthMinScanned)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].Rel != violations[j].Rel {
			return violations[i].Rel < violations[j].Rel
		}
		return violations[i].Line < violations[j].Line
	})
	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleWalkDepthFuncDeclChildren, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.Rel, v.Line, v.Message)
		}
	}
	assert.Empty(t, violations,
		"%s: %d site(s) listed above. ast.FuncDecl is depth-1; replace "+
			"EachInSubtree[ast.FuncDecl] / EachInSubtreeStopAt[ast.FuncDecl] with "+
			"EachInChildren[ast.FuncDecl].",
		ruleWalkDepthFuncDeclChildren, len(violations))
}

// walkDepthLoadFixture returns this rule's diagnostics for a single fixture
// file under tools/archtest/internal/walkdepthfixtures/<name>.go, reusing the
// SAME pure detector + typed *types.Info source as the live dogfood (no
// syntactic fallback drift surface). caseName is the basename without .go.
func walkDepthLoadFixture(t *testing.T, caseName string) []Diagnostic {
	t.Helper()
	target := "tools/archtest/internal/walkdepthfixtures/" + caseName + ".go"
	var result []Diagnostic
	found := false
	_ = Run(t, Typed(TypedOpts{Tests: true}, []string{"./tools/archtest/internal/walkdepthfixtures"}),
		func(p *Pass) []Diagnostic {
			for _, f := range p.Files {
				rel := p.Rel(f)
				if rel != target {
					continue
				}
				result = collectWalkDepthFuncDeclViolations(p.TypesInfo, p.Fset, f, rel)
				found = true
			}
			return nil
		})
	require.True(t, found,
		"walkDepthLoadFixture: fixture %s not loaded — ensure it exists under "+
			"tools/archtest/internal/walkdepthfixtures/", target)
	return result
}

// TestWalkDepthFuncDeclChildren01_Fixtures is the reverse self-check: it proves
// the detector fires for each banned form and stays silent for the GREEN
// baselines (depth-1 EachInChildren + a legitimate subtree walk of a nesting
// node). RED/GREEN share the one pure detector + typed source as the live scan.
func TestWalkDepthFuncDeclChildren01_Fixtures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		caseName string
		wantHits int
	}{
		{"red_eachinsubtree_funcdecl", 1},
		{"red_eachinsubtreestopat_funcdecl", 1},
		{"red_archtest_facade_eachinsubtree_funcdecl", 1},
		{"green_eachinchildren_funcdecl", 0},
		{"green_eachinsubtree_callexpr", 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.caseName, func(t *testing.T) {
			t.Parallel()
			diags := walkDepthLoadFixture(t, tc.caseName)
			require.Len(t, diags, tc.wantHits,
				"%s fixture %s: got %d hits want %d (%v)",
				ruleWalkDepthFuncDeclChildren, tc.caseName, len(diags), tc.wantHits, diags)
			for _, d := range diags {
				assert.Contains(t, d.Message, ruleWalkDepthFuncDeclChildren)
			}
		})
	}
}

// TestWalkDepthFuncDeclChildren01_AntiVacuity asserts the banned-func set is
// non-empty and every entry has a non-empty remedy — a removed/cleared map
// would make the live scan vacuously green.
func TestWalkDepthFuncDeclChildren01_AntiVacuity(t *testing.T) {
	t.Parallel()
	require.NotEmpty(t, walkDepthBannedSubtreeFuncs,
		"%s: banned subtree-axis set is empty — the rule would never fire",
		ruleWalkDepthFuncDeclChildren)
	for banned, remedy := range walkDepthBannedSubtreeFuncs {
		assert.NotEmpty(t, banned, "%s: empty banned func name", ruleWalkDepthFuncDeclChildren)
		assert.NotEmpty(t, remedy, "%s: banned %q has no remedy", ruleWalkDepthFuncDeclChildren, banned)
	}
}
