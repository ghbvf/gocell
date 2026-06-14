// test_polling_external_reason_literal.go — importable TEST-POLLING-EXTERNAL-REASON-LITERAL-01 rule logic (#1640 M3 PR-9).
//
// Non-test home for TEST-POLLING-EXTERNAL-REASON-LITERAL-01 scanner logic, so it
// can be compiled and run by an external Cell repository.
//
// Dogfooded by tools/archtest/test_polling_external_reason_literal_test.go:
//
//	TestExternalReasonLiteral → Report(t, "TEST-POLLING-EXTERNAL-REASON-LITERAL-01", CheckTestwaitExternalReasonLiteral(...))
//
// # Register status
//
// Not registered in StandardCellRules: this rule's scan scope (testwait package)
// is gocell-internal layout specific with no ConfigForExternalCell consumer-extension
// → vacuous/false-red externally.
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

const ruleTestPollingExternalReasonLiteral01 = "TEST-POLLING-EXTERNAL-REASON-LITERAL-01"

// testwaitPkgPath is the canonical import path of the testwait package.
const testwaitPkgPath = PlatformFrameworkModulePath + "/pkg/testutil/testwait"

// testwaitExternalFunc is the name of the typed-marker function whose callsites
// this archtest locks.
const testwaitExternalFunc = "External"

// testwaitReasonFormat is the required format for the reason argument: kebab-case
// identifier (lowercase letters, digits, and hyphens, starting with a lowercase
// letter). Snake_case, PascalCase, single-char, and leading-hyphen strings all fail.
var testwaitReasonFormat = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)

// testwaitReasonPlaceholder matches reason literals that are placeholder
// identifiers (todo / fixme / tbd / xxx / placeholder / wip / hack / temp /
// test / dummy) optionally followed by a hyphen and more text. These are
// rejected because they provide no descriptive information about the polling
// site. "test" and "dummy" are included because they leak test scaffolding
// as permanent reason labels; "hack" and "temp" signal explicitly temporary
// explanations that should be replaced before merging.
var testwaitReasonPlaceholder = regexp.MustCompile(`^(todo|fixme|tbd|xxx|placeholder|wip|hack|temp|test|dummy)(-|$)`)

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

// CheckTestwaitExternalReasonLiteral enforces TEST-POLLING-EXTERNAL-REASON-LITERAL-01
// module-wide and returns diagnostics for every violation found. The caller should
// pass the results to Report(t, "TEST-POLLING-EXTERNAL-REASON-LITERAL-01", diags).
//
// Two-load coverage (mirror PANIC-REGISTERED-01 plumbing): Load 1 (tags=nil)
// catches reverse build directives; Load 2 (FlatNonDefaultTags) catches all
// forward-tagged files in one union. Test-variant load (Tests:true) included
// in both so *_test.go callers of testwait.External are scanned.
//
//nolint:gocognit // R2-approved: two-load scan + dedup + sort, linear additive, not nesting.
func CheckTestwaitExternalReasonLiteral(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

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

	_ = Run(t, Typed(TypedOpts{Tests: true}, []string{"./..."}), scan)
	_ = Run(t, Typed(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, []string{"./..."}), scan)

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
