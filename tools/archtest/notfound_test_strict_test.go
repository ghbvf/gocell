package archtest

// INVARIANT: POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01
//
// notfound_test_strict_test.go — typed function-call funnel guard for
// `_NotFound` tests.
//
// Rule statement: any Go function declared as `func Test.*_NotFound(...)` or
// any t.Run("..._NotFound", ...) table case must contain at least one
// CallExpr whose Fun resolves via *types.Info to
//   - github.com/ghbvf/gocell/pkg/errcode/errcodetest.AssertCode, or
//   - github.com/ghbvf/gocell/pkg/errcode/errcodetest.AssertWireCode,
//
// and whose `expected` argument is a SelectorExpr that resolves to a typed
// errcode.Code constant whose value matches ^ERR_.*_NOT_FOUND$.
//
// Hard property comes from form uniqueness: picking any other shape (no
// funnel call, wrong callee, BasicLit expected, non-NotFound code) fails
// archtest in CI. See cap-14:18 (PR238-FU4 / PR#553 Hard upgrade) and
// .claude/rules/gocell/ai-collab.md §"Hard 范本" / "typed function call
// as Hard funnel for unbounded operations" — template is panicregister.Approved
// + PANIC-REGISTERED-01 (panic_invariants_test.go).
//
// Tool blind-spot disclosure (charter §3):
//   - Cross-function helper wrappers — a project-local helper that calls
//     errcodetest.AssertCode internally instead of the test calling it
//     directly — not detected at the test site. PR-a / PR-b contain no
//     such wrappers (storetest conformance suites were migrated to inline
//     funnel calls in PR-b). Future PRs introducing one must extend the
//     funnel callee allowlist in this file plus register a function-level
//     carve-out in ADR docs/architecture/202605121800-adr-archtest-carveout-narrow.md.
//     Any new approved funnel callee added to notFoundFunnelExpectedArgIdx must
//     be registered in ADR docs/architecture/202605121800-adr-archtest-carveout-narrow.md
//     registry table within the same PR; ERRCODE-CARVEOUT-ADR-CONSISTENCY-01
//     Hard守卫 will reject silent map mutations.
//   - Generic test functions `Test*[T]_NotFound`: not exercised; revisit
//     if a first instance lands.
//   - Cross-package re-export of the funnel: if any package re-declares
//     AssertCode as a wrapper, the wrapper escapes detection. Defense
//     = the callee allowlist is explicit (PkgPath + Name) and edited
//     only via ADR-tracked PR.
//   - Panic-based NotFound tests: out of scope. NotFound is an
//     error-return semantic; panic shape is governed by PANIC-REGISTERED-01.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleNotFoundTestStrict = "POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01"

// errcodetestPkgPath is the canonical import path of the errcodetest funnel
// package. Only callees in this package are accepted by the rule.
const errcodetestPkgPath = "github.com/ghbvf/gocell/pkg/errcode/errcodetest"

// notFoundCodeErrcodePkgPath is the package path of typed errcode.Code
// constants. Used to validate that the `expected` argument resolves through
// *types.Info to a constant declared in this package.
const notFoundCodeErrcodePkgPath = "github.com/ghbvf/gocell/pkg/errcode"

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
	scanner.EachInSubtreeStopAt[ast.CallExpr](body, stopAtNestedFuncLit, func(call *ast.CallExpr) {
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
					"expected argument (selector form, not BasicLit). See cap-14:18.",
				name,
			),
		})
	}

	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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

	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
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

// TestNotFoundTestStrict enforces POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01
// module-wide via the Pass-Driver funnel (RunTyped + Tests:true so _test.go
// files participate in the typed load). Walks every Go file (production +
// test) and emits a violation for each _NotFound test site that does not
// contain at least one compliant errcodetest funnel call.
//
// All strict `_NotFound$` sites in the corpus already route through the
// funnel after PR-a (211-pg-notfound-test-migration) and PR-b's storetest
// inline migration (runtime/audit/ledger/storetest/suite.go +
// runtime/auth/session/storetest/suite.go); this test must pass.
func TestNotFoundTestStrict(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []notFoundViolation

	// FlatNonDefaultTags() returns the union of all distinct non-empty build
	// tags across KnownNonDefaultTags() groups, sorted. Using FlatNonDefaultTags
	// + single RunTyped (clock_invariants_test.go pattern) collapses N
	// packages.Load calls into 1; KnownNonDefaultTags loop (panic_invariants
	// pattern) does N parallel loads with full *types.Info cached per call,
	// accumulating RSS that pushes GHA 2-CPU 7GB runners into OOM SIGTERM and
	// running 7× wall-time (43s observed on CI run 25971613882 shard 14 vs
	// 20s slowgate budget). The flat-tag union covers every conditional-build
	// _test.go that any group would visit; for a presence-check rule like this
	// one (the rule only cares whether SOME tag-group sees a violation), the
	// flat union is semantically equivalent to the loop-and-merge form.
	//
	// Fixture packages live under tools/archtest/testdata/ and are exercised
	// via the t.Run("Fixtures", ...) subtest below with pure-AST mode. They
	// are skipped from the module-wide scan via shouldSkipForNotFoundStrict
	// regardless of build tags.
	opts := TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}
	_ = RunTyped(t, opts, []string{"./..."}, func(p *Pass) []Diagnostic {
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

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleNotFoundTestStrict, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s — %s", v.File, v.Line, v.Name, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: every _NotFound test must call errcodetest.AssertCode or "+
			"errcodetest.AssertWireCode with a typed errcode.Err*NotFound expected. "+
			"See pkg/errcode/errcodetest and docs/backlog/cap-14-tooling.md.",
		ruleNotFoundTestStrict)
}

// TestNotFoundTestStrictFixtures verifies the rule logic against static
// fixture packages under tools/archtest/testdata/notfound_test_strict_fixtures/.
// Each subdir contains a single usage.go declaring one _NotFound
// site; the parent test asserts the precise line(s) flagged (or 0 for
// compliant fixtures).
//
// Fixture mode uses pure-AST scanning (Run + DirsScope) because the
// fixtures are deliberately non-buildable in isolation (they import real
// errcode and errcodetest packages but exist outside any module). The
// rule's info == nil fallback branch in callSatisfiesFunnelRule covers the
// AST-only form lock. The errcode-pattern type resolution branch is
// exercised by TestNotFoundTestStrict against live production code.
func TestNotFoundTestStrictFixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	fixtureBase := filepath.Join(root, "tools", "archtest", "testdata", "notfound_test_strict_fixtures")

	cases := []struct {
		dir       string
		wantLines []int // empty = GREEN (0 violations); non-empty = RED with these line numbers
	}{
		// GREEN — funnel call with typed errcode SelectorExpr.
		{"compliant_funcdecl_green", nil},
		{"compliant_trun_green", nil},
		{"compliant_wire_green", nil},

		// RED — no funnel call. fn at line 12.
		{"missing_funnel_red", []int{12}},
		// RED — funnel-shaped name but wrong callee (assert.Equal not
		// errcodetest). fn at line 15.
		{"wrong_callee_red", []int{15}},
		// RED — right funnel, but expected resolves to non-NotFound errcode.
		// In pure-AST mode the AST fallback only checks the selector Sel
		// name pattern (Err*NotFound); ErrValidationFailed fails that
		// pattern. fn at line 15.
		{"wrong_code_pattern_red", []int{15}},
		// RED — right funnel, expected is CallExpr (errcode.Code("ERR_X"))
		// not SelectorExpr; form lock rejects. fn at line 15.
		{"basic_lit_expected_red", []int{15}},
		// RED — HTTP handler test only asserts status, no funnel call.
		// fn at line 14.
		{"status_only_red", []int{14}},

		// RED — t.Run("..._NotFound", helperFunc) with non-inline body.
		// Fail-closed: helper function references cannot be statically
		// verified for funnel-call presence. t.Run at line 17.
		{"non_inline_body_red", []int{17}},
		// RED — t.Run name contains '/' (subtest path separator). The
		// strings.HasSuffix predicate accepts any Go-legal subtest name
		// ending in _NotFound; the regex-only predicate would have
		// missed this. t.Run at line 15.
		{"trun_slash_case_red", []int{15}},
		// RED — funnel call lives inside a nested *ast.FuncLit (dead
		// closure) that is never invoked. EachInSubtreeStopAt boundary
		// must NOT credit the call. fn at line 17.
		{"nested_funclit_red", []int{17}},
		// RED — expected arg is errcode.Code("ERR_X_NOT_FOUND") CallExpr
		// conversion form. Same SelectorExpr form lock as
		// basic_lit_expected_red; the named-type guard for production
		// code is exercised by TestNotFoundTestStrict against real
		// errcode imports (typed scan). fn at line 31.
		{"wrong_const_type_red", []int{31}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()

			fixtureDir := filepath.Join(fixtureBase, tc.dir)
			scope := DirsScope(fixtureDir, []string{"."})

			var violations []notFoundViolation
			Run(t, scope, func(p *Pass) []Diagnostic {
				for _, f := range p.Files {
					rel := p.Rel(f)
					violations = append(violations,
						scanFileForNotFoundViolations(p.Fset, f, p.TypesInfo, rel)...)
				}
				return nil
			})

			var gotLines []int
			for _, v := range violations {
				gotLines = append(gotLines, v.Line)
			}
			sort.Ints(gotLines)

			if tc.wantLines == nil {
				assert.Empty(t, gotLines,
					"fixture %s expected 0 violations, got at lines %v", tc.dir, gotLines)
				return
			}
			assert.Equal(t, tc.wantLines, gotLines,
				"fixture %s expected violations at lines %v, got %v", tc.dir, tc.wantLines, gotLines)
		})
	}
}

// TestNotFoundTestStrict_RegexPredicates is a defense-in-depth blind-spot
// self-test: it pins notFoundFuncNamePattern / tRunCaseNameMatches /
// notFoundCodePattern against a table of accept/reject examples so future
// authors do not silently widen or narrow the rule scope. Pair with
// TestNotFoundTestStrictFixtures (form/value) — this test catches regex /
// predicate drift at the rule-scope level.
func TestNotFoundTestStrict_RegexPredicates(t *testing.T) {
	t.Parallel()

	t.Run("FuncName", func(t *testing.T) {
		accept := []string{
			"TestFoo_NotFound",
			"TestHandler_HandleGet_NotFound",
			"TestConfigRepository_Update_NotFound",
			"TestA_NotFound",
		}
		reject := []string{
			"TestFoo",                     // no _NotFound suffix
			"TestFoo_NotFound_Returns404", // suffixed past _NotFound
			"TestFoo_NotFoundIgnored",     // _NotFound is not the suffix
			"Test_NotFound",               // no body between Test and _NotFound
			"NotATest_NotFound",           // missing Test prefix
			"TestFoo_notfound",            // lowercase
			"helper_NotFound",             // missing Test prefix
			"",                            // empty string
			"NotFound",                    // no Test prefix, no _ separator
		}
		for _, name := range accept {
			assert.True(t, notFoundFuncNamePattern.MatchString(name),
				"funcName should accept %q", name)
		}
		for _, name := range reject {
			assert.False(t, notFoundFuncNamePattern.MatchString(name),
				"funcName should reject %q", name)
		}
	})

	t.Run("TRunCaseName", func(t *testing.T) {
		accept := []string{
			"GetByKey_NotFound",
			"Toggle_NotFound",
			"Sub_NotFound",
			"A_NotFound",
			// F-2: Go testing allows any non-empty string as a subtest
			// name. Names with /, spaces, hyphens, or unicode used to
			// be silently missed by the regex `^[A-Za-z0-9_]+_NotFound$`;
			// the suffix-only predicate accepts them.
			"Get/missing_NotFound",      // slash (subtest path separator)
			"nested case foo_NotFound",  // space
			"with-hyphen-name_NotFound", // hyphen
			"日本語subtest_NotFound",       // non-ASCII
		}
		reject := []string{
			"GetByKey_NotFound_Returns404", // suffixed past _NotFound
			"GetByKey",                     // no _NotFound suffix
			"_NotFound",                    // suffix only (no body before)
			"GetByKey_notfound",            // lowercase
			"",                             // empty string
			"NotFound",                     // no _ separator
		}
		for _, name := range accept {
			assert.True(t, tRunCaseNameMatches(name),
				"tRunCaseName should accept %q", name)
		}
		for _, name := range reject {
			assert.False(t, tRunCaseNameMatches(name),
				"tRunCaseName should reject %q", name)
		}
	})

	t.Run("NotFoundCodePattern", func(t *testing.T) {
		accept := []string{
			"ERR_SESSION_NOT_FOUND",
			"ERR_CONFIG_REPO_NOT_FOUND",
			"ERR_FLAG_NOT_FOUND",
			"ERR_AUTH_USER_NOT_FOUND",
			"ERR_AUDIT_LEDGER_NOT_FOUND",
		}
		reject := []string{
			"ERR_VALIDATION_FAILED",  // not NotFound
			"ERR_NOT_FOUND",          // missing module segment
			"err_session_not_found",  // lowercase
			"ERR_SESSION_NOT_FOUND_", // trailing underscore
			"ERR_NotFound",           // mixed case
		}
		for _, code := range accept {
			assert.True(t, notFoundCodePattern.MatchString(code),
				"codePattern should accept %q", code)
		}
		for _, code := range reject {
			assert.False(t, notFoundCodePattern.MatchString(code),
				"codePattern should reject %q", code)
		}
	})
}

func TestShouldSkipForNotFoundStrict(t *testing.T) {
	t.Parallel()
	cases := []struct {
		rel  string
		skip bool
	}{
		{"tools/archtest/testdata/notfound_test_strict_fixtures/missing_funnel_red/usage.go", true},
		{"vendor/foo/bar.go", true},
		{"worktrees/211/foo.go", true},
		{".git/info/exclude.go", true},
		{"node_modules/foo.go", true},
		{"testdata/bar.go", true},
		{"runtime/auth/session/storetest/suite.go", false},
		{"cells/configcore/slices/configread/handler_test.go", false},
		{"pkg/errcode/errcodetest/assertions.go", false},
	}
	for _, tc := range cases {
		got := shouldSkipForNotFoundStrict(tc.rel)
		if got != tc.skip {
			t.Errorf("shouldSkipForNotFoundStrict(%q) = %t, want %t", tc.rel, got, tc.skip)
		}
	}
}
