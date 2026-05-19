package archtest

// invariants:
//   - INVARIANT: TEST-POLLING-EXTERNAL-REASON-LITERAL-01
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
// Upstream funnel closure — banning bare require.Eventually / assert.Eventually
// in test code — is deferred to a follow-up PR per
// docs/plans/202605181600-042-archtest.md §1.1 (TEST-EVENTUALLY-FUNNEL-01).
// Until then this rule is "Hard downstream + Soft upstream" transitional;
// AI-rebust §"Funnel 双向锁评级" explicitly permits this with backlog
// registration, anchored at plan §1.1.

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
)

const ruleTestPollingExternalReasonLiteral01 = "TEST-POLLING-EXTERNAL-REASON-LITERAL-01"

// testwaitPkgPath is the canonical import path of the testwait package.
const testwaitPkgPath = "github.com/ghbvf/gocell/pkg/testutil/testwait"

// testwaitExternalFunc is the name of the typed-marker function whose callsites
// this archtest locks.
const testwaitExternalFunc = "External"

// testwaitReasonFormat is the required format for the reason argument: kebab-case
// identifier (lowercase letters, digits, and hyphens, starting with a lowercase
// letter). Snake_case, PascalCase, single-char, and leading-hyphen strings all fail.
var testwaitReasonFormat = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)

// testwaitReasonPlaceholder matches reason literals that are placeholder
// identifiers (todo / fixme / tbd / xxx / placeholder / wip) optionally followed
// by a hyphen and more text. These are rejected because they provide no
// descriptive information about the polling site.
var testwaitReasonPlaceholder = regexp.MustCompile(`^(todo|fixme|tbd|xxx|placeholder|wip)(-|$)`)

type testwaitExternalViolation struct {
	File   string
	Line   int
	Reason string
}

// scanFileForTestwaitExternalViolations walks one AST file and returns
// violations of TEST-POLLING-EXTERNAL-REASON-LITERAL-01.
//
// info must be the *types.Info bound to the same packages.Load that produced
// the file (guaranteed when passing pass.TypesInfo). info == nil falls back to
// pure-AST selector-name matching (fixture mode without type resolution).
func scanFileForTestwaitExternalViolations(
	fset *token.FileSet,
	file *ast.File,
	info *types.Info,
	rel string,
) []testwaitExternalViolation {
	var violations []testwaitExternalViolation

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isTestwaitExternalCallee(call.Fun, info) {
			return
		}
		// External(t, reason, condition, timeout, tick, msgAndArgs...) — args[1] is reason.
		if len(call.Args) < 2 {
			violations = append(violations, testwaitExternalViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: "testwait.External requires at least two arguments (t, reason, ...)",
			})
			return
		}

		reasonArg := call.Args[1]

		// Rule 1: reason must be *ast.BasicLit with Kind == token.STRING.
		// This is the strict (callee, arg) form-uniqueness Hard lock — same
		// shape as PANIC-REGISTERED-01 reason argument enforcement: const
		// idents, fmt.Sprintf, concatenation, and any other expression form
		// are rejected, so the reason is always visible at the callsite
		// without hopping to a declaration.
		reasonLit, ok := reasonArg.(*ast.BasicLit)
		if !ok || reasonLit.Kind != token.STRING {
			violations = append(violations, testwaitExternalViolation{
				File: rel,
				Line: fset.Position(call.Pos()).Line,
				Reason: "testwait.External reason must be a const string literal " +
					"(no const ident / fmt.Sprintf / concat / variable)",
			})
			return
		}

		// Rule 2: reason literal must match kebab-case identifier format.
		reasonVal, err := strconv.Unquote(reasonLit.Value)
		if err != nil || !testwaitReasonFormat.MatchString(reasonVal) {
			violations = append(violations, testwaitExternalViolation{
				File: rel,
				Line: fset.Position(call.Pos()).Line,
				Reason: fmt.Sprintf(
					"testwait.External reason must be kebab-case identifier (got: %s)",
					reasonLit.Value,
				),
			})
			return
		}

		// Rule 3: reason must not be a placeholder identifier.
		if testwaitReasonPlaceholder.MatchString(reasonVal) {
			violations = append(violations, testwaitExternalViolation{
				File: rel,
				Line: fset.Position(call.Pos()).Line,
				Reason: fmt.Sprintf(
					"reason %q is a placeholder identifier (todo/fixme/tbd/xxx/placeholder/wip);"+
						" replace with descriptive kebab-case",
					reasonVal,
				),
			})
			return
		}
	})

	return violations
}

// isTestwaitExternalCallee reports whether funExpr is the callee testwait.External.
//
// When info is non-nil, resolution is via *types.Info.Uses so import aliases
// (e.g. `import tw "…/testwait"; tw.External(...)`) are handled correctly.
// When info is nil, falls back to pure-AST selector-name matching (used in
// fixture mode without full type resolution).
func isTestwaitExternalCallee(funExpr ast.Expr, info *types.Info) bool {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	if sel.Sel.Name != testwaitExternalFunc {
		return false
	}
	if info != nil {
		obj := info.Uses[sel.Sel]
		if obj == nil {
			return false
		}
		fn, ok := obj.(*types.Func)
		if !ok || fn.Pkg() == nil {
			return false
		}
		return fn.Pkg().Path() == testwaitPkgPath && fn.Name() == testwaitExternalFunc
	}
	// AST-only fallback: match "<x>.External" where x identifier is "testwait".
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return xIdent.Name == "testwait"
}

// shouldSkipForTestwaitExternal returns true for paths excluded from the
// scan. Generated code, vendored libraries, archtest's own self-tests, and
// the testwait package itself are skipped — testwait/*_test.go uses its own
// public API with deliberately diverse reason values that should not be
// gated by this rule (testwait_test.go covers it via its own assertions).
func shouldSkipForTestwaitExternal(rel string) bool {
	switch {
	case strings.HasPrefix(rel, "vendor/"):
		return true
	case strings.HasPrefix(rel, "generated/"):
		return true
	case strings.HasPrefix(rel, "tools/archtest/testdata/"):
		return true
	case strings.HasPrefix(rel, "worktrees/"):
		return true
	case strings.HasPrefix(rel, ".git/"):
		return true
	case strings.HasPrefix(rel, "node_modules/"):
		return true
	case strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/"):
		return true
	case rel == "pkg/testutil/testwait/testwait_test.go":
		return true
	}
	return false
}

// TestExternalReasonLiteral enforces TEST-POLLING-EXTERNAL-REASON-LITERAL-01
// module-wide. Scans both production and test sources (Tests: true) because
// testwait.External is a test-helper API whose callers live in *_test.go.
//
// Tool: archtest.RunTyped + *types.Info callee resolution + go/ast literal
// shape check. This is the typed-marker funnel downstream lock; the upstream
// lock (ban bare require.Eventually / assert.Eventually) is a separate rule
// landing in PR3 per docs/plans/202605181600-042-archtest.md §1.1.
//
// Blind spots of the chosen tool (per AI-rebust §"工具选定后强制盲区自检"):
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

	seen := make(map[string]struct{}) // dedup across two loads by "rel:line:msg"
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
			for _, v := range scanFileForTestwaitExternalViolations(p.Fset, file, p.TypesInfo, rel) {
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

	// Two-load coverage (mirror PANIC-REGISTERED-01 plumbing): Load 1 (tags=nil)
	// catches reverse build directives; Load 2 (ProductionFlatTags) catches all
	// forward-tagged files in one union. Test-variant load (Tests:true) included
	// in both so *_test.go callers of testwait.External are scanned.
	_ = RunTyped(t, TypedOpts{Tests: true}, []string{"./..."}, scan)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: ProductionFlatTags()}, []string{"./..."}, scan)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleTestPollingExternalReasonLiteral01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: every testwait.External call must pass a const kebab-case string literal "+
			"as args[1]. See pkg/testutil/testwait and docs/plans/202605181600-042-archtest.md §1.1.",
		ruleTestPollingExternalReasonLiteral01)
}

// TestExternalReasonLiteralFixtures verifies the rule logic against static
// fixture packages under tools/archtest/testdata/testwait_external_fixtures/.
// Each fixture dir owns a diag.golden capturing the rule's real output.
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
		"reason_format_invalid_red",
		"reason_placeholder_red",
	}

	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/testwait_external_fixtures/" + dir

			diags := RunTypedFixture(t, FixtureOpts{}, []string{fixturePattern},
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
// self-test required by AI-rebust §"工具选定后强制盲区自检".
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

	_ = RunTyped(t, TypedOpts{Tests: true}, []string{"./..."}, scan)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: ProductionFlatTags()}, []string{"./..."}, scan)

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

// isExternalIdentUse reports whether ident's *types.Info.Uses entry resolves
// to testwait.External. Used by the blind-spot self-test.
func isExternalIdentUse(ident *ast.Ident, info *types.Info) bool {
	if ident == nil || info == nil {
		return false
	}
	return isExternalIdentObj(info.Uses[ident])
}

// isExternalIdentObj reports whether obj is the testwait.External function.
func isExternalIdentObj(obj types.Object) bool {
	if obj == nil {
		return false
	}
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == testwaitPkgPath && fn.Name() == testwaitExternalFunc
}
