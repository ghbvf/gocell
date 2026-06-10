//go:build archtest

package archtest

// INVARIANT: TEST-POLLING-EXTERNAL-REASON-LITERAL-01
//
// test_polling_external_reason_literal_test.go — Hard downstream funnel for
// pkg/testutil/testwait.External:
//
//   - Every callsite of testwait.External MUST pass an *ast.BasicLit STRING
//     literal as args[1] (the reason argument), value matching the kebab-case
//     identifier format ^[a-z][a-z0-9-]+$ and not a placeholder token.
//   - Every reference to testwait.External in the module MUST appear in
//     *ast.CallExpr.Fun position (direct invocation). Indirect references
//     (function-variable assignment, function-pointer pass to other helpers,
//     reflect.ValueOf, etc.) are rejected by the blind-spot reverse self-test.
//
// Rule logic (const / scanner helpers / CheckTestwaitExternalReasonLiteral)
// lives in the non-test home test_polling_external_reason_literal.go so an
// external Cell repository can compile and run it (Go does not compile
// _test.go into importable packages). This file keeps only the dogfood,
// fixtures, and reverse self-tests.
//
// Upstream funnel closure is locked by sibling archtest
// TEST-EVENTUALLY-FUNNEL-01 (test_eventually_funnel_test.go): bans bare
// require.Eventually / assert.Eventually / *WithT module-wide so reaching
// testwait.External is the only Go-source path to synchronous polling.
// Together with this downstream rule they form a Hard funnel
// (ai-robust.md §"Funnel 双向锁评级") and Hard 范本 "typed marker funnel
// for unbounded ops" (sibling of panicregister.Approved).

import (
	"fmt"
	"go/ast"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExternalReasonLiteral dogfoods TEST-POLLING-EXTERNAL-REASON-LITERAL-01
// module-wide via CheckTestwaitExternalReasonLiteral. The check scans both
// production and test sources (Tests: true) because testwait.External is a
// test-helper API whose callers live in *_test.go.
//
// Tool: Run(t, Typed(...)) + *types.Info callee resolution + go/ast literal
// shape check. This is the typed-marker funnel downstream lock; the upstream
// lock (ban bare require.Eventually / assert.Eventually) lives in sibling
// archtest TEST-EVENTUALLY-FUNNEL-01 (test_eventually_funnel_test.go).
//
// Blind spots of the chosen tool (per AI-robust §"工具选定后强制盲区自检"):
//
//   - Indirect call via function variable: var f = testwait.External; f(t, dynVar, ...).
//     The CallExpr.Fun here is an *ast.Ident pointing at a variable, not a
//     SelectorExpr — isTestwaitExternalCallee returns false, no callsite check.
//   - Function-pointer pass-through: someHelper(testwait.External).
//   - Method-value binding: (&s{}).field = testwait.External.
//   - Reflect call: reflect.ValueOf(testwait.External).Call(...).
//
// All four shapes silently bypass the (callee, arg) form-uniqueness check.
// TestExternalReasonLiteral_NoIndirectReferences below is the reverse
// self-test: it scans every Ident that *types.Info.Uses resolves to
// testwait.External and asserts the Ident appears in *ast.CallExpr.Fun
// position (direct invocation only). Hard funnel form-uniqueness is the
// AND of the main rule + this reverse self-test.
func TestExternalReasonLiteral(t *testing.T) {
	t.Parallel()
	Report(t, ruleTestPollingExternalReasonLiteral01,
		CheckTestwaitExternalReasonLiteral(t, ConfigForExternalCell{}))
}

// TestExternalReasonLiteralFixtures verifies the rule logic against static
// fixture packages under tools/archtest/testdata/testwait_external_fixtures/.
// Each fixture dir owns a diag.golden capturing the rule's real output.
//
// To regenerate golden files: go test ./tools/archtest/... -run TestExternalReasonLiteralFixtures$ -update.
func TestExternalReasonLiteralFixtures(t *testing.T) {
	t.Parallel()

	dirs := []string{
		// GREEN — expect 0 violations (empty golden).
		"positive_green",
		// RED cases — expect violations.
		"reason_variable_red",
		"reason_sprintf_red",
		"reason_concat_red",
		"reason_const_ident_red",
		// reason_cross_pkg_const_red proves that cross-package SelectorExpr
		// const references (helper.ReasonConst) are rejected by the
		// "not BasicLit" check, complementing reason_const_ident_red (same-pkg
		// Ident). See the archtest blind-spot analysis in TestExternalReasonLiteral
		// godoc: both paths hit the same rejection branch.
		"reason_cross_pkg_const_red",
		"reason_format_invalid_red",
		"reason_placeholder_red",
	}

	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/testwait_external_fixtures/" + dir

			diags := Run(t, Fixture(FixtureOpts{}, []string{fixturePattern}),
				func(p *Pass) []Diagnostic {
					if p.TypesInfo == nil || p.Fset == nil {
						return nil
					}
					var out []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)
						for _, v := range scanFileForTestwaitExternalViolations(p.Fset, file, p.TypesInfo, rel) {
							out = append(out, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
						}
					}
					return out
				})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"testwait_external_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}

// TestExternalReasonLiteral_NoIndirectReferences is the blind-spot reverse
// self-test required by AI-robust §"工具选定后强制盲区自检".
//
// It scans every *ast.Ident in production + test code whose *types.Info.Uses
// entry is testwait.External, then asserts the Ident appears in
// *ast.CallExpr.Fun position. Any reference outside that position — function
// variable assignment, function-pointer pass-through, method-value binding,
// reflect.ValueOf — is rejected, closing the four blind spots of the main
// rule's CallExpr-driven scan.
//
// Together with TestExternalReasonLiteral this forms the (callee, arg)
// form-uniqueness Hard lock: outside of direct invocation `pkg.External(...)`
// no syntactic shape can reach the External symbol from non-testwait code.
func TestExternalReasonLiteral_NoIndirectReferences(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []testwaitExternalViolation

	scan := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if shouldSkipForTestwaitExternal(rel) {
				continue
			}
			// Pass 1: gather CallExpr.Fun positions whose Fun resolves to
			// testwait.External. These are the "legal" reference sites.
			legalCallFun := make(map[*ast.Ident]struct{})
			EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil {
					return
				}
				if !isExternalIdentUse(sel.Sel, p.TypesInfo) {
					return
				}
				legalCallFun[sel.Sel] = struct{}{}
			})

			// Pass 2: every Ident in types.Info.Uses pointing at External
			// must be in the legalCallFun set; otherwise it's an indirect
			// reference.
			for ident, obj := range p.TypesInfo.Uses {
				if ident == nil || obj == nil {
					continue
				}
				if !isExternalIdentObj(obj) {
					continue
				}
				// Make sure this ident belongs to the current file.
				identFile := p.Fset.Position(ident.Pos()).Filename
				absFile := p.Abs(file)
				if identFile != absFile {
					continue
				}
				if _, ok := legalCallFun[ident]; ok {
					continue
				}
				v := testwaitExternalViolation{
					File: rel,
					Line: p.Fset.Position(ident.Pos()).Line,
					Reason: "indirect reference to testwait.External (function value, " +
						"pointer pass-through, method binding, or reflect); only direct " +
						"call testwait.External(...) is permitted",
				}
				key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Reason)
				if _, dup := seen[key]; dup {
					continue
				}
				seen[key] = struct{}{}
				violations = append(violations, v)
			}
		}
		return nil
	}

	_ = Run(t, Typed(TypedOpts{Tests: true}, []string{"./..."}), scan)
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, []string{"./..."}), scan)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s blind-spot self-test: %d indirect reference(s):",
			ruleTestPollingExternalReasonLiteral01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s blind-spot reverse self-test: testwait.External must only appear in "+
			"direct call position; indirect references bypass the reason-literal check.",
		ruleTestPollingExternalReasonLiteral01)
}

// TestExternalReasonLiteral_NoIndirectReferences_Fixtures proves the indirect
// reference scanner is wired correctly by loading each of the four RED fixture
// packages and asserting the diagnostics match the expected golden output.
// Without these RED fixtures, TestExternalReasonLiteral_NoIndirectReferences
// would pass even if the scanner logic regressed (current tree has no indirect
// references, so the module-wide test is always green regardless of scanner
// health).
//
// Fixture categories:
//   - indirect_var_red:          var f = testwait.External
//   - indirect_funcarg_red:      passThrough(testwait.External)
//   - indirect_reflect_red:      reflect.ValueOf(testwait.External)
//   - indirect_struct_field_red: wrapper{F: testwait.External}
func TestExternalReasonLiteral_NoIndirectReferences_Fixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	fixtures := []string{
		"indirect_var_red",
		"indirect_funcarg_red",
		"indirect_reflect_red",
		"indirect_struct_field_red",
	}

	for _, dir := range fixtures {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/testwait_external_fixtures/" + dir

			diags := Run(t, Fixture(FixtureOpts{}, []string{fixturePattern}),
				func(p *Pass) []Diagnostic {
					if p.TypesInfo == nil || p.Fset == nil {
						return nil
					}
					var out []Diagnostic
					for _, file := range p.Files {
						rel := p.Rel(file)

						legalCallFun := make(map[*ast.Ident]struct{})
						EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
							sel, ok := call.Fun.(*ast.SelectorExpr)
							if !ok || sel.Sel == nil {
								return
							}
							if !isExternalIdentUse(sel.Sel, p.TypesInfo) {
								return
							}
							legalCallFun[sel.Sel] = struct{}{}
						})

						for ident, obj := range p.TypesInfo.Uses {
							if ident == nil || obj == nil {
								continue
							}
							if !isExternalIdentObj(obj) {
								continue
							}

							identFile := p.Fset.Position(ident.Pos()).Filename
							absFile := p.Abs(file)
							if identFile != absFile {
								continue
							}
							if _, ok := legalCallFun[ident]; ok {
								continue
							}
							out = append(out, Diagnostic{
								Rel:  rel,
								Line: p.Fset.Position(ident.Pos()).Line,
								Message: "indirect reference to testwait.External (function value, " +
									"pointer pass-through, method binding, or reflect); only direct " +
									"call testwait.External(...) is permitted",
							})
						}
					}

					sort.Slice(out, func(i, j int) bool {
						if out[i].Rel != out[j].Rel {
							return out[i].Rel < out[j].Rel
						}
						return out[i].Line < out[j].Line
					})
					return out
				})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"testwait_external_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}
