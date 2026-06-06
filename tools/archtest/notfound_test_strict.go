// notfound_test_strict.go — importable POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01 rule logic (#1640 M3 PR-9).
//
// Non-test home for POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01 scanner logic, so it
// can be compiled and run by an external Cell repository.
//
// Dogfooded by tools/archtest/notfound_test_strict_test.go:
//
//	TestNotFoundTestStrict → Report(t, "POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01", CheckNotFoundTestStrict(...))
//
// # Register status
//
// Not registered in StandardCellRules: this rule's scan scope (errcodetest +
// errcode packages) is gocell-internal layout specific with no
// ConfigForExternalCell consumer-extension → vacuous/false-red externally.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const ruleNotFoundTestStrict = "POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01"

// errcodetestPkgPath is the canonical import path of the errcodetest funnel
// package. Only callees in this package are accepted by the rule.
const errcodetestPkgPath = PlatformModulePath + "/pkg/errcode/errcodetest"

// notFoundCodeErrcodePkgPath is the package path of typed errcode.Code
// constants. Used to validate that the `expected` argument resolves through
// *types.Info to a constant declared in this package.
const notFoundCodeErrcodePkgPath = PlatformModulePath + "/pkg/errcode"

// notFoundCodeTypeName is the unqualified name of the typed-string named
// type that all errcode.Err* constants share. The rule's type check
// requires the const's static type to be this named type — not a sibling
// named string-typed const in the same package — to anchor the form to
// the specific declared sentinel surface.
const notFoundCodeTypeName = "Code"

// notFoundCodePattern matches the value of an errcode.Code constant that
// represents a NotFound sentinel (e.g. ERR_SESSION_NOT_FOUND,
// ERR_CONFIG_REPO_NOT_FOUND, ERR_FLAG_NOT_FOUND). All current Err*NotFound
// constants in pkg/errcode satisfy this pattern.
var notFoundCodePattern = regexp.MustCompile(`^ERR_[A-Z0-9_]+_NOT_FOUND$`)

// notFoundFuncNamePattern matches Test* function decls whose name ends in
// _NotFound. A leading "Test" is required (Go test convention); the body
// between Test and _NotFound may contain letters, digits, and underscores,
// but must begin with a letter (rejects "Test_NotFound" which has no
// distinguishing identifier between Test and the suffix).
var notFoundFuncNamePattern = regexp.MustCompile(`^Test[A-Za-z][A-Za-z0-9_]*_NotFound$`)

// notFoundTRunSuffix is the literal suffix that selects a t.Run table case
// for this rule. Go's testing package allows any non-empty string as a
// subtest name (slashes for /-run filter, spaces, hyphens, unicode all
// permitted), so an explicit suffix check accepts every name the test
// runtime would accept — strictly more general than a regex over a fixed
// character class (which would silently miss `Get/missing_NotFound`,
// `nested case foo_NotFound`, etc.).
const notFoundTRunSuffix = "_NotFound"

// tRunCaseNameMatches reports whether a t.Run table-case name selects into
// this rule. The name must end in _NotFound AND have at least one character
// before the suffix (rejecting the degenerate `_NotFound` literal with no
// distinguishing identifier).
func tRunCaseNameMatches(name string) bool {
	if !strings.HasSuffix(name, notFoundTRunSuffix) {
		return false
	}
	return len(name) > len(notFoundTRunSuffix)
}

// notFoundFunnelExpectedArgIdx is the 0-based position of the `expected
// errcode.Code` parameter inside each approved funnel callee's parameter
// list. Keys are the SelectorExpr.Sel.Name of the callee; the rule's
// type-aware resolution then verifies the callee package path matches
// errcodetestPkgPath.
var notFoundFunnelExpectedArgIdx = map[string]int{
	"AssertCode":     2, // (t, err, expected)
	"AssertWireCode": 3, // (t, rec, expectedStatus, expected)
}

// notFoundViolation records a single rule breach: the FuncDecl / t.Run site
// missing any compliant funnel call. We aggregate by site rather than per-call
// because the rule is "at least one funnel call must satisfy", not "every
// call must satisfy".
type notFoundViolation struct {
	File   string
	Line   int
	Name   string // function name or t.Run sub-case name
	Reason string
}

// stopAtNestedFuncLit is the boundary predicate that excludes funnel calls
// inside dead closures. A `_NotFound` test that contains a funnel call only
// inside a nested *ast.FuncLit cannot statically prove that closure runs —
// crediting such calls would be fail-open (the canonical mutation-test
// hazard the rule exists to forbid).
//
// Boundary semantics: ANY *ast.FuncLit beneath the root body stops descent.
// Root itself is exempt by scanner.EachInSubtreeStopAt contract.
func stopAtNestedFuncLit(n ast.Node) bool {
	_, ok := n.(*ast.FuncLit)
	return ok
}

// findFunnelCallSitesInBlock returns the set of CallExpr nodes inside body
// whose callee selector name appears in notFoundFunnelExpectedArgIdx,
// EXCLUDING any call inside a nested *ast.FuncLit boundary (dead closure).
// The slice is pre-filtered by name only; full typed-callee resolution
// happens in callSatisfiesFunnelRule via *types.Info.
//
// Uses scanner.EachInSubtreeStopAt — the third typed-function-choice-for-
// walk-depth member (depth=full + boundary), picked over EachInSubtree
// (which would credit nested-FuncLit calls = fail-open) and EachInChildren
// (which would miss multi-stmt nested-block real call sites). Picking the
// wrong walker is a typed-API-name mistake at the call site, not a hidden
// AST behavior.
func findFunnelCallSitesInBlock(body *ast.BlockStmt) []*ast.CallExpr {
	var sites []*ast.CallExpr
	EachInSubtreeStopAt[ast.CallExpr](body, stopAtNestedFuncLit, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		if _, allowed := notFoundFunnelExpectedArgIdx[sel.Sel.Name]; !allowed {
			return
		}
		sites = append(sites, call)
	})
	return sites
}

// callSatisfiesFunnelRule reports whether call resolves to one of the
// approved funnel functions AND its `expected` argument is a SelectorExpr
// resolving (via *types.Info) to a const in pkg/errcode whose value matches
// notFoundCodePattern.
//
// When info is nil (fixture pure-AST mode), the rule falls back to:
//   - AST-only callee match by selector base ident name "errcodetest"
//   - expected arg must be a SelectorExpr whose Sel.Name starts with "Err"
//     and ends with "NotFound"
//
// In both modes the expected argument MUST be a SelectorExpr — BasicLit
// (e.g. "ERR_SESSION_NOT_FOUND") and explicit CallExpr conversions like
// `errcode.Code("ERR_X")` are rejected by form even when their evaluated
// value matches the NotFound pattern. This form-lock is the rule's anti-
// drift against authors who route around the typed sentinel.
//
//nolint:gocognit,cyclop // R2-approved: linear additive type-resolution gates (each rejects one bypass path), not nesting.
func callSatisfiesFunnelRule(info *types.Info, call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	expectedArgIdx, allowed := notFoundFunnelExpectedArgIdx[sel.Sel.Name]
	if !allowed {
		return false
	}

	if info != nil {
		obj := info.Uses[sel.Sel]
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return false
		}
		if fn.Pkg().Path() != errcodetestPkgPath {
			return false
		}
	} else {
		xIdent, ok := sel.X.(*ast.Ident)
		if !ok || xIdent.Name != "errcodetest" {
			return false
		}
	}

	if len(call.Args) <= expectedArgIdx {
		return false
	}
	expectedArg := call.Args[expectedArgIdx]

	expectedSel, ok := expectedArg.(*ast.SelectorExpr)
	if !ok {
		return false
	}

	if info != nil {
		obj := info.Uses[expectedSel.Sel]
		c, ok := obj.(*types.Const)
		if !ok || c.Pkg() == nil {
			return false
		}
		if c.Pkg().Path() != notFoundCodeErrcodePkgPath {
			return false
		}
		// Verify the const's static type is the named type errcode.Code.
		// Without this check, a sibling const in the same pkg with a
		// different named type but a value matching notFoundCodePattern
		// (e.g. a hypothetical errcode.Kind = "ERR_X_NOT_FOUND") would
		// pass the value-pattern gate. Form-locking the named type
		// closes that drift surface.
		named, ok := c.Type().(*types.Named)
		if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
			return false
		}
		if named.Obj().Pkg().Path() != notFoundCodeErrcodePkgPath ||
			named.Obj().Name() != notFoundCodeTypeName {
			return false
		}
		v, ok := EvaluateConstString(info, expectedArg)
		if !ok {
			return false
		}
		return notFoundCodePattern.MatchString(v)
	}

	name := expectedSel.Sel.Name
	return strings.HasPrefix(name, "Err") && strings.HasSuffix(name, "NotFound")
}

// scanFileForNotFoundViolations walks a single AST file and returns
// violations of POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01.
//
// Two selection paths:
//  1. FuncDecl whose name matches notFoundFuncNamePattern
//     (^Test[A-Za-z][A-Za-z0-9_]*_NotFound$).
//  2. t.Run("..._NotFound", func(t *testing.T) { ... }) where the literal
//     case name passes tRunCaseNameMatches (suffix "_NotFound" with a
//     non-empty prefix; Go's testing package allows any non-empty string
//     as a subtest name, so the suffix predicate accepts every name the
//     test runtime accepts).
//
// For each selected body, at least one CallExpr must satisfy
// callSatisfiesFunnelRule. Failing that, a single violation is emitted at
// the FuncDecl or t.Run line.
//
//nolint:gocognit,cyclop // R2-approved: two flat sequential AST-walk passes; FuncLit closures inflate count, not nesting.
func scanFileForNotFoundViolations(
	fset *token.FileSet,
	file *ast.File,
	info *types.Info,
	rel string,
) []notFoundViolation {
	var violations []notFoundViolation

	emitMissingFunnel := func(pos token.Pos, name string) {
		violations = append(violations, notFoundViolation{
			File: rel,
			Line: fset.Position(pos).Line,
			Name: name,
			Reason: fmt.Sprintf(
				"[POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01] %s missing typed funnel: every test whose name ends in _NotFound "+
					"must contain at least one call to errcodetest.AssertCode or "+
					"errcodetest.AssertWireCode with a typed errcode.Err*NotFound "+
					"expected argument (selector form, not BasicLit).",
				name,
			),
		})
	}

	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil || fn.Recv != nil {
			return
		}
		if !notFoundFuncNamePattern.MatchString(fn.Name.Name) {
			return
		}
		funnelCalls := findFunnelCallSitesInBlock(fn.Body)
		satisfied := false
		for _, call := range funnelCalls {
			if callSatisfiesFunnelRule(info, call) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			emitMissingFunnel(fn.Pos(), fn.Name.Name)
		}
	})

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isTRunCall(call, info) || len(call.Args) < 2 {
			return
		}
		nameLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || nameLit.Kind != token.STRING {
			return
		}
		caseName, err := strconv.Unquote(nameLit.Value)
		if err != nil || !tRunCaseNameMatches(caseName) {
			return
		}
		// Fail-closed on non-inline body: t.Run("..._NotFound", runHelper)
		// or t.Run("..._NotFound", makeCase(args)) are cross-function helper
		// forms that the static AST scan cannot verify for funnel-call
		// presence. Emitting a violation forces inline FuncLit bodies as
		// the only sanctioned shape — closing the "helper escape" hole
		// that was previously declared a tool blind spot but silently
		// skipped (fail-open).
		funLit, ok := call.Args[1].(*ast.FuncLit)
		if !ok || funLit.Body == nil {
			emitMissingFunnel(call.Pos(),
				`t.Run("`+caseName+`", <non-inline body>)`)
			return
		}
		funnelCalls := findFunnelCallSitesInBlock(funLit.Body)
		satisfied := false
		for _, c := range funnelCalls {
			if callSatisfiesFunnelRule(info, c) {
				satisfied = true
				break
			}
		}
		if !satisfied {
			emitMissingFunnel(call.Pos(), `t.Run("`+caseName+`", ...)`)
		}
	})

	return violations
}

// isTRunCall reports whether call is `t.Run(name, body)` on a Go testing
// receiver — *testing.T, *testing.B, or *testing.F. The receiver
// identifier is not constrained at the AST level (commonly `t`, but could
// be aliased); identity is established via *types.Info if available.
//
// Type resolution path (when info != nil): the selector base must resolve
// to a Named type in the standard `testing` package with name T, B, or F.
// testing.TB is an interface that does NOT declare Run — Run is owned by
// T / B / F concretely — so this rejects `TB.Run(...)` synthetic calls
// (none exist in the corpus, but the predicate must reject in principle
// to be type-aware Hard).
//
// Pure-AST fallback (info == nil, fixture mode): accepts any `.Run`
// selector by name. Documented as a known fixture-mode relaxation;
// production scan is the type-aware path.
func isTRunCall(call *ast.CallExpr, info *types.Info) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Run" {
		return false
	}
	if info == nil {
		// Fixture pure-AST mode: name-only match.
		return true
	}
	recvType := info.TypeOf(sel.X)
	if recvType == nil {
		return false
	}
	if ptr, ok := recvType.(*types.Pointer); ok {
		recvType = ptr.Elem()
	}
	named, ok := recvType.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return false
	}
	if named.Obj().Pkg().Path() != "testing" {
		return false
	}
	switch named.Obj().Name() {
	case "T", "B", "F":
		return true
	}
	return false
}

// shouldSkipForNotFoundStrict returns true for paths excluded from the
// module-wide scan. Test fixtures live under testdata/ and are exercised
// separately by TestNotFoundTestStrictFixtures.
func shouldSkipForNotFoundStrict(rel string) bool {
	switch {
	case strings.HasPrefix(rel, "vendor/"):
		return true
	case strings.HasPrefix(rel, "worktrees/"):
		return true
	case strings.HasPrefix(rel, ".git/"):
		return true
	case strings.HasPrefix(rel, "node_modules/"):
		return true
	case strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/"):
		return true
	}
	return false
}

// notFoundDiagsFromPass collects violations across every file in pass that
// passes the path filter, deduping by (rel, line, name) within a single Pass
// invocation. The driver-level dedup-by-*ast.File pointer guards against
// the same file being scanned twice across test-variant package overlap.
func notFoundDiagsFromPass(p *Pass) []notFoundViolation {
	if p == nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []notFoundViolation
	for _, f := range p.Files {
		rel := p.Rel(f)
		if shouldSkipForNotFoundStrict(rel) {
			continue
		}
		for _, v := range scanFileForNotFoundViolations(p.Fset, f, p.TypesInfo, rel) {
			key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Name)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// CheckNotFoundTestStrict enforces POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01
// module-wide and returns diagnostics for every violation found. The caller should
// pass the results to Report(t, "POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01", diags).
func CheckNotFoundTestStrict(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	seen := make(map[string]struct{})
	var violations []notFoundViolation

	opts := TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}
	_ = Run(t, Typed(opts, []string{"./..."}), func(p *Pass) []Diagnostic {
		for _, v := range notFoundDiagsFromPass(p) {
			key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Name)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			violations = append(violations, v)
		}
		return nil
	})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	var diags []Diagnostic
	for _, v := range violations {
		diags = append(diags, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
	}
	return diags
}
