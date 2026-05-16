package archtest

// invariants:
//   - INVARIANT: PANIC-REDACT-01
//   - INVARIANT: PANIC-REGISTERED-01
//
// panic_invariants_test.go — consolidated AST guards for panic-related invariants.
//
// Invariants covered:
//
//	PANIC-REDACT-01     slog.Any("panic", X) must wrap X with redaction.RedactAny(...)
//	PANIC-REGISTERED-01 every production panic() call must wrap its argument with
//	                    panicregister.Approved(reason, value), where reason is a
//	                    const string literal. See pkg/panicregister and
//	                    docs/architecture/202604270030-architectural-panic-whitelist.md.

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

	"github.com/stretchr/testify/assert"
)

// INVARIANT: PANIC-REDACT-01
//
// TestPanicLogMustUseRedactAny enforces that every slog.Any("panic", X) call
// in production code wraps X with redaction.RedactAny(...). This prevents
// panic values containing DSNs, tokens, or credentials from reaching log sinks
// un-redacted.
//
// Rule ID: PANIC-REDACT-01
// Wave 0: fails against the current codebase (11 violations in Wave 0).
// Wave 3: all violations remediated; white-list stays empty permanently.
func TestPanicLogMustUseRedactAny(t *testing.T) {
	root := findModuleRoot(t)
	scope := ModuleScope(root,
		ExcludeRels("tools/archtest/doc.go"),
	)

	diags := Run(t, scope, func(p *Pass) []Diagnostic {
		var out []Diagnostic
		for _, f := range p.Files {
			EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Any" {
					return
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "slog" {
					return
				}
				if len(call.Args) < 2 {
					return
				}
				// First arg must be string literal "panic".
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || lit.Value != `"panic"` {
					return
				}
				// Second arg must be a call to redaction.RedactAny(...).
				arg := call.Args[1]
				argCall, ok := arg.(*ast.CallExpr)
				if !ok {
					out = append(out, Diagnostic{
						Rel:     p.Rel(f),
						Line:    p.Fset.Position(call.Pos()).Line,
						Message: `slog.Any("panic", X) must wrap X with redaction.RedactAny(...)`,
					})
					return
				}
				argSel, ok := argCall.Fun.(*ast.SelectorExpr)
				if !ok || argSel.Sel.Name != "RedactAny" {
					out = append(out, Diagnostic{
						Rel:     p.Rel(f),
						Line:    p.Fset.Position(call.Pos()).Line,
						Message: `slog.Any("panic", X) must wrap X with redaction.RedactAny(...)`,
					})
				}
			})
		}
		return out
	})
	Report(t, "PANIC-REDACT-01", diags)
}

// INVARIANT: PANIC-REGISTERED-01
//
// TestPanicRegistered enforces PANIC-REGISTERED-01: every production panic()
// call must wrap its argument with panicregister.Approved(reason, value),
// where reason is a const string literal. See pkg/panicregister and
// docs/architecture/202604270030-architectural-panic-whitelist.md.

const rulePanicRegistered01 = "PANIC-REGISTERED-01"

// panicregisterPkgPath is the canonical import path of the panicregister package.
const panicregisterPkgPath = "github.com/ghbvf/gocell/pkg/panicregister"

// panicregisterApprovedFunc is the name of the only approved funnel function.
const panicregisterApprovedFunc = "Approved"

// panicRegisteredReasonFormat is the required format for the reason argument
// to panicregister.Approved: kebab-case identifier (lowercase letters, digits,
// and hyphens, starting with a lowercase letter). Snake_case, PascalCase,
// single-char, and leading-hyphen strings all fail.
var panicRegisteredReasonFormat = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)

// panicRegisteredReasonPlaceholder matches reason literals that are
// placeholder identifiers (todo / fixme / tbd / xxx / placeholder / wip)
// optionally followed by a hyphen and more text. These are rejected because
// they provide no descriptive information about the panic site.
var panicRegisteredReasonPlaceholder = regexp.MustCompile(`^(todo|fixme|tbd|xxx|placeholder|wip)(-|$)`)

// errcodePkgPath is the canonical import path of the errcode package,
// used by payloadTypeAllowed to verify the payload is *errcode.Error.
const errcodePkgPath = "github.com/ghbvf/gocell/pkg/errcode"

// payloadTypeAllowed returns true when the static type of arg satisfies the
// PANIC-REGISTERED-01 payload constraint:
//
//   - *errcode.Error — produced by errcode.Assertion or upstream constructors
//     returning *errcode.Error (A/B-class panics)
//   - empty interface / any — produced by recover() for C-class re-throws
//
// Any other type (bare error, string, fmt.Errorf return value, etc.) is
// rejected. When info is nil the check is skipped (fixtures without full type
// resolution fall back to AST-only mode and this guard is inactive).
func payloadTypeAllowed(info *types.Info, arg ast.Expr) bool {
	if info == nil {
		return true // can't verify; don't false-flag
	}
	t := info.TypeOf(arg)
	if t == nil {
		return true
	}
	// Allow *pkg/errcode.Error
	if ptr, ok := t.(*types.Pointer); ok {
		if named, ok := ptr.Elem().(*types.Named); ok {
			obj := named.Obj()
			if obj != nil && obj.Pkg() != nil &&
				obj.Pkg().Path() == errcodePkgPath && obj.Name() == "Error" {
				return true
			}
		}
	}
	// Allow empty interface (interface{} / any) — recover() return type
	if iface, ok := t.Underlying().(*types.Interface); ok && iface.NumMethods() == 0 {
		return true
	}
	return false
}

type panicRegisteredViolation struct {
	File   string
	Line   int
	Reason string
}

// scanFileForPanicViolations walks a single AST file and returns violations
// of PANIC-REGISTERED-01. info is required for callee resolution; if nil,
// the scan falls back to pure-AST selector matching (used in fixture mode
// where full type resolution is provided separately).
func scanFileForPanicViolations(
	fset *token.FileSet,
	file *ast.File,
	info *types.Info,
	rel string,
) []panicRegisteredViolation {
	var violations []panicRegisteredViolation

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isPanicCallExpr(call) {
			return
		}
		if len(call.Args) == 0 {
			// panic() with no args is a compile error, but guard anyway.
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: "panic argument must be a call to panicregister.Approved(literal, value)",
			})
			return
		}

		arg := call.Args[0]

		// Rule 1: arg must be a CallExpr.
		argCall, ok := arg.(*ast.CallExpr)
		if !ok {
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: "panic argument must be a call to panicregister.Approved(literal, value)",
			})
			return
		}

		// Rule 2: the callee of argCall must resolve to panicregister.Approved.
		if !isApprovedCallee(argCall.Fun, info) {
			calleeName := formatCallee(argCall.Fun)
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: fmt.Sprintf("panic argument must call panicregister.Approved (got: %s)", calleeName),
			})
			return
		}

		// Rule 3: Approved must have at least 2 args.
		if len(argCall.Args) < 2 {
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: "panicregister.Approved requires two arguments",
			})
			return
		}

		// Rule 4: first arg (reason) must be a *ast.BasicLit with Kind == token.STRING.
		reasonArg := argCall.Args[0]
		reasonLit, ok := reasonArg.(*ast.BasicLit)
		if !ok || reasonLit.Kind != token.STRING {
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: "panicregister.Approved reason must be a const string literal (no fmt.Sprintf / concat / variable)",
			})
			return
		}

		// Rule 5: reason literal must match kebab-case identifier format.
		reasonVal, err := strconv.Unquote(reasonLit.Value)
		if err != nil || !panicRegisteredReasonFormat.MatchString(reasonVal) {
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: fmt.Sprintf("panicregister.Approved reason must be kebab-case identifier (got: %s)", reasonLit.Value),
			})
			return
		}

		// Rule 6: reason must not be a placeholder identifier.
		if panicRegisteredReasonPlaceholder.MatchString(reasonVal) {
			violations = append(violations, panicRegisteredViolation{
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

		// Rule 7: payload (Args[1]) must be *errcode.Error or interface{}.
		payloadArg := argCall.Args[1]
		if !payloadTypeAllowed(info, payloadArg) {
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: fmt.Sprintf("panicregister.Approved payload must be *errcode.Error or interface{} (got: %s)", info.TypeOf(payloadArg)),
			})
			return
		}
	})

	return violations
}

// isApprovedCallee reports whether funExpr refers to panicregister.Approved.
// When info is non-nil, resolution is via types.Info.Uses for correctness
// under import aliasing. When info is nil, falls back to pure-AST selector
// name matching.
func isApprovedCallee(funExpr ast.Expr, info *types.Info) bool {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	if sel.Sel.Name != panicregisterApprovedFunc {
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
		return fn.Pkg().Path() == panicregisterPkgPath && fn.Name() == panicregisterApprovedFunc
	}
	// AST-only fallback: match "panicregister.Approved".
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return xIdent.Name == "panicregister"
}

// formatCallee returns a short human-readable description of the callee
// for use in violation messages.
func formatCallee(funExpr ast.Expr) string {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok {
		return "<unknown>"
	}
	xIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return "?." + sel.Sel.Name
	}
	return xIdent.Name + "." + sel.Sel.Name
}

// isPanicCallExpr reports whether call is a call to the built-in panic function.
func isPanicCallExpr(call *ast.CallExpr) bool {
	ident, ok := call.Fun.(*ast.Ident)
	return ok && ident.Name == "panic"
}

// shouldSkipForPanicRegistered returns true for paths that must not be
// scanned by TestPanicRegistered (test files, generated code, testdata, etc.).
// Mirrors fileroles.IsProductionCode exclusions for paths that RunTyped may
// surface but PANIC-REGISTERED-01 should not gate.
func shouldSkipForPanicRegistered(rel string) bool {
	switch {
	case strings.HasSuffix(rel, "_test.go"):
		return true
	case strings.HasPrefix(rel, "vendor/"):
		return true
	case strings.HasPrefix(rel, "generated/"):
		return true
	case strings.HasPrefix(rel, "examples/"):
		return true
	case strings.HasPrefix(rel, "tools/archtest/"):
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

// TestPanicRegistered enforces PANIC-REGISTERED-01 module-wide.
//
// NOTE: All call-site migrations complete as of PR #467; this test must pass.
func TestPanicRegistered(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}) // dedup violations by "rel:line:msg"
	var violations []panicRegisteredViolation

	for _, tagGroup := range KnownNonDefaultTags() {
		// skip archtest_fixture — fixtures intentionally violate rules
		if containsTag(tagGroup, "archtest_fixture") {
			continue
		}
		// RunTyped with "./..." loads the whole module; the rule scans
		// hand-written production panic sites only. shouldSkipForPanicRegistered
		// excludes generated/, examples/, tools/archtest/, testdata/, _test.go
		// (mirroring fileroles.IsProductionCode) at the file level. generated/
		// is intentionally excluded — codegen templates are the single source
		// guaranteeing emitted panics use panicregister.Approved, so scanning
		// them would be redundant (no enforcement gap).
		_ = RunTyped(t, TypedOpts{Tags: tagGroup}, []string{"./..."}, func(p *Pass) []Diagnostic {
			if p.TypesInfo == nil || p.Fset == nil {
				return nil
			}
			for _, file := range p.Files {
				rel := p.Rel(file)
				if shouldSkipForPanicRegistered(rel) {
					continue
				}
				for _, v := range scanFileForPanicViolations(p.Fset, file, p.TypesInfo, rel) {
					key := fmt.Sprintf("%s:%d:%s", v.File, v.Line, v.Reason)
					if _, dup := seen[key]; dup {
						continue
					}
					seen[key] = struct{}{}
					violations = append(violations, v)
				}
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
		t.Logf("%s: %d violation(s):", rulePanicRegistered01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: every production panic() call must use panic(panicregister.Approved(literal, value)). "+
			"See pkg/panicregister and docs/architecture/202604270030-architectural-panic-whitelist.md.",
		rulePanicRegistered01)
}

// containsTag reports whether tag appears in the given build tag group.
func containsTag(group []string, tag string) bool {
	for _, t := range group {
		if t == tag {
			return true
		}
	}
	return false
}

// TestPanicRegisteredScannerFixtures verifies the PANIC-REGISTERED-01 rule
// logic against static fixture packages under
// tools/archtest/testdata/panic_registered_fixtures/.
func TestPanicRegisteredScannerFixtures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		dir       string
		wantLines []int // empty = GREEN (0 violations); non-empty = RED with these line numbers
	}{
		// RED cases — expect violations.
		{"bare_string_red", []int{6}},
		{"non_funnel_err_red", []int{6}},
		{"non_literal_reason_red", []int{13}},
		{"old_errcode_form_red", []int{8}},
		{"must_prefix_bare_red", []int{7}},

		// GREEN cases — expect 0 violations.
		{"assertion_wrapped_green", nil},
		{"recovered_value_green", nil},
		{"must_prefix_wrapped_green", nil},

		// RED cases for reason argument shape.
		{"reason_const_ident_red", []int{15}},
		{"reason_format_invalid_red", []int{12, 16}},

		// RED/GREEN cases for payload type guard (RC-C1).
		{"payload_type_invalid_red", []int{14, 19, 23}}, // 3 violations: fmt.Errorf, string var, string literal
		{"payload_type_valid_green", nil},               // *errcode.Error and interface{} are allowed (no violations)

		// RED cases for reason placeholder denylist (RC-B1).
		{"reason_placeholder_red", []int{12, 16, 20}}, // todo / fixme / wip all rejected
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/panic_registered_fixtures/" + tc.dir

			var violations []panicRegisteredViolation
			// Load using module root so imports of panicregister/errcode resolve.
			_ = RunTyped(t, TypedOpts{}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil || p.Fset == nil {
					return nil
				}
				for _, file := range p.Files {
					rel := p.Rel(file)
					violations = append(violations, scanFileForPanicViolations(p.Fset, file, p.TypesInfo, rel)...)
				}
				return nil
			})

			var gotLines []int
			for _, v := range violations {
				gotLines = append(gotLines, v.Line)
			}
			sort.Ints(gotLines)

			wantLines := append([]int(nil), tc.wantLines...)
			sort.Ints(wantLines)

			assert.Equal(t, wantLines, gotLines,
				"fixture %s: violation lines mismatch (got violations: %+v)", tc.dir, violations)
		})
	}
}
