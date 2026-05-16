package archtest

// INVARIANT: POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01
//
// notfound_test_strict_test.go — typed function-call funnel guard for
// `_NotFound` tests.
//
// INVARIANT: any Go function declared as `func Test.*_NotFound(...)` or
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
//   - Non-testing.T `.Run` methods: `isTRunCall` matches any `*.Run(...)`
//     selector by name. Custom types with a `Run(string, fn) bool/error`
//     signature whose first arg is a `_NotFound` string literal would be
//     flagged as if they were `t.Run`. The current corpus has no such
//     type; if a future PR adds one, either rename the method to avoid
//     collision or extend isTRunCall to type-resolve the receiver to
//     testing.TB via *types.Info.

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

// notFoundTRunNamePattern matches t.Run table-case names that end in
// _NotFound. The leading "Test" prefix is NOT required here — the table-case
// name is independent of the parent function name (e.g. parent is
// TestRepo_Errors with sub-case GetByKey_NotFound).
var notFoundTRunNamePattern = regexp.MustCompile(`^[A-Za-z0-9_]+_NotFound$`)

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

// findFunnelCallSitesInBlock returns the set of CallExpr nodes inside body
// whose callee selector name appears in notFoundFunnelExpectedArgIdx. The
// slice is pre-filtered by name only; full typed-callee resolution happens
// in callSatisfiesFunnelRule via *types.Info.
func findFunnelCallSitesInBlock(body *ast.BlockStmt) []*ast.CallExpr {
	var sites []*ast.CallExpr
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
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
//  1. FuncDecl whose name matches ^Test[A-Za-z][A-Za-z0-9_]*_NotFound$.
//  2. t.Run("..._NotFound", func(t *testing.T) { ... }) where the literal
//     case name matches ^[A-Za-z0-9_]+_NotFound$.
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
		if !isTRunCall(call) || len(call.Args) < 2 {
			return
		}
		nameLit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || nameLit.Kind != token.STRING {
			return
		}
		caseName, err := strconv.Unquote(nameLit.Value)
		if err != nil || !notFoundTRunNamePattern.MatchString(caseName) {
			return
		}
		funLit, ok := call.Args[1].(*ast.FuncLit)
		if !ok || funLit.Body == nil {
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

// isTRunCall reports whether call is `t.Run(name, body)`. The receiver
// identifier is not constrained (commonly `t`, but could be aliased).
func isTRunCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return sel.Sel.Name == "Run"
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

	for _, tagGroup := range KnownNonDefaultTags() {
		// Fixture packages live under tools/archtest/testdata/ and are
		// exercised by TestNotFoundTestStrictFixtures with pure-AST mode.
		// They are skipped from the module-wide scan via
		// shouldSkipForNotFoundStrict regardless of build tags.
		opts := TypedOpts{Tests: true, Tags: tagGroup}
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
	}

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
// Each subdir contains a single usage_fixture.go declaring one _NotFound
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
		// errcodetest). fn at line 14.
		{"wrong_callee_red", []int{14}},
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
// self-test: it pins notFoundFuncNamePattern / notFoundTRunNamePattern /
// notFoundCodePattern against a table of accept/reject examples so future
// authors do not silently widen or narrow the rule scope. Pair with
// TestNotFoundTestStrictFixtures (form/value) — this test catches regex
// drift at the predicate level.
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
		}
		reject := []string{
			"GetByKey_NotFound_Returns404", // suffixed past _NotFound
			"GetByKey",                     // no _NotFound suffix
			"_NotFound",                    // leading underscore only
			"GetByKey_notfound",            // lowercase
			"",                             // empty string
			"NotFound",                     // no _ separator
		}
		for _, name := range accept {
			assert.True(t, notFoundTRunNamePattern.MatchString(name),
				"tRunCaseName should accept %q", name)
		}
		for _, name := range reject {
			assert.False(t, notFoundTRunNamePattern.MatchString(name),
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
