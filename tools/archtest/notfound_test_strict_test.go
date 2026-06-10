//go:build archtest

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
// archtest in CI. See
// .claude/rules/gocell/ai-robust.md §"Hard 范本" / "typed function call
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
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNotFoundTestStrict enforces POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01
// module-wide via the Pass-Driver funnel (Run(t, Typed(...)) with Tests:true so _test.go
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
	Report(t, ruleNotFoundTestStrict, CheckNotFoundTestStrict(t, ConfigForExternalCell{}))
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

	// Each fixture dir owns a diag.golden capturing the rule's real output
	// (Rel:Line: Message); GREEN fixtures have an empty golden. Expected line
	// numbers live in the regenerated golden, never in this table. See ADR
	// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
	dirs := []string{
		// GREEN — funnel call with typed errcode SelectorExpr.
		"compliant_funcdecl_green", "compliant_trun_green", "compliant_wire_green",
		// RED — no funnel call.
		"missing_funnel_red",
		// RED — funnel-shaped name but wrong callee.
		"wrong_callee_red",
		// RED — right funnel, but expected resolves to non-NotFound errcode.
		"wrong_code_pattern_red",
		// RED — right funnel, expected is CallExpr not SelectorExpr; form lock rejects.
		"basic_lit_expected_red",
		// RED — HTTP handler test only asserts status, no funnel call.
		"status_only_red",
		// RED — t.Run with non-inline body; fail-closed.
		"non_inline_body_red",
		// RED — t.Run name contains '/' (subtest path separator).
		"trun_slash_case_red",
		// RED — funnel call inside nested *ast.FuncLit (dead closure).
		"nested_funclit_red",
		// RED — expected arg is errcode.Code("ERR_X_NOT_FOUND") CallExpr form.
		"wrong_const_type_red",
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixtureDir := filepath.Join(fixtureBase, dir)
			scope := DirsScope(fixtureDir, []string{"."})

			var violations []notFoundViolation
			Run(t, AST(scope), func(p *Pass) []Diagnostic {
				for _, f := range p.Files {
					rel := p.Rel(f)
					violations = append(violations,
						scanFileForNotFoundViolations(p.Fset, f, p.TypesInfo, rel)...)
				}
				return nil
			})

			var diags []Diagnostic
			for _, v := range violations {
				diags = append(diags, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
			}

			goldenPath := filepath.Join(fixtureBase, dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
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
