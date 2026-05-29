package archtest

// panic_invariants.go — importable PANIC-REGISTERED-01 rule logic.
//
// This is the non-test home of the PANIC-REGISTERED-01 scanner so it can be
// compiled and run by an external Cell repository through [CheckPanicRegistered]
// / [StandardCellRules] (Go never compiles a dependency's _test.go, so rule
// logic that external repos must run cannot live in a _test.go file). GoCell's
// own TestPanicRegistered (panic_invariants_test.go) calls the same
// CheckPanicRegistered — single source, no parallel rule body.
//
// Platform-symbol paths (panicregister.Approved, *errcode.Error) are anchored
// to [PlatformModulePath] (fixed: external repos import these packages as a
// GoCell dependency at that path). The scan SCOPE is the running module,
// supplied by RunTyped → findModuleRoot. See external.go for the design.

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

const rulePanicRegistered01 = "PANIC-REGISTERED-01"

// panicregisterPkgPath is the canonical import path of the panicregister
// package — a GoCell platform symbol path, anchored to PlatformModulePath so a
// module rename updates exactly one place and no bare literal appears here.
const panicregisterPkgPath = PlatformModulePath + "/pkg/panicregister"

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

// errcodePkgPath is the canonical import path of the errcode package, used by
// payloadTypeAllowed to verify the payload is *errcode.Error — a GoCell
// platform symbol path, anchored to PlatformModulePath.
const errcodePkgPath = PlatformModulePath + "/pkg/errcode"

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
		if reason, bad := panicCallProblem(call, info); bad {
			violations = append(violations, panicRegisteredViolation{
				File:   rel,
				Line:   fset.Position(call.Pos()).Line,
				Reason: reason,
			})
		}
	})
	return violations
}

// panicCallProblem checks a single panic(...) call against the seven
// PANIC-REGISTERED-01 rules and returns the FIRST violation message (matching
// the original short-circuit-on-first-failure behavior), or ("", false) when
// the call is compliant.
func panicCallProblem(call *ast.CallExpr, info *types.Info) (string, bool) {
	if len(call.Args) == 0 {
		// panic() with no args is a compile error, but guard anyway.
		return "panic argument must be a call to panicregister.Approved(literal, value)", true
	}
	// Rule 1: arg must be a CallExpr.
	argCall, ok := call.Args[0].(*ast.CallExpr)
	if !ok {
		return "panic argument must be a call to panicregister.Approved(literal, value)", true
	}
	// Rule 2: the callee of argCall must resolve to panicregister.Approved.
	if !isApprovedCallee(argCall.Fun, info) {
		return fmt.Sprintf("panic argument must call panicregister.Approved (got: %s)", formatCallee(argCall.Fun)), true
	}
	// Rule 3: Approved must have at least 2 args.
	if len(argCall.Args) < 2 {
		return "panicregister.Approved requires two arguments", true
	}
	// Rules 4-6: reason argument shape.
	if reason, bad := panicReasonProblem(argCall.Args[0]); bad {
		return reason, true
	}
	// Rule 7: payload (Args[1]) must be *errcode.Error or interface{}.
	if payloadArg := argCall.Args[1]; !payloadTypeAllowed(info, payloadArg) {
		return fmt.Sprintf("panicregister.Approved payload must be *errcode.Error or interface{} (got: %s)", info.TypeOf(payloadArg)), true
	}
	return "", false
}

// panicReasonProblem validates the reason argument to panicregister.Approved
// (rules 4-6: const string literal, kebab-case format, not a placeholder) and
// returns the first violation message or ("", false) when compliant.
func panicReasonProblem(reasonArg ast.Expr) (string, bool) {
	// Rule 4: must be a *ast.BasicLit with Kind == token.STRING.
	reasonLit, ok := reasonArg.(*ast.BasicLit)
	if !ok || reasonLit.Kind != token.STRING {
		return "panicregister.Approved reason must be a const string literal (no fmt.Sprintf / concat / variable)", true
	}
	// Rule 5: reason literal must match kebab-case identifier format.
	reasonVal, err := strconv.Unquote(reasonLit.Value)
	if err != nil || !panicRegisteredReasonFormat.MatchString(reasonVal) {
		return fmt.Sprintf("panicregister.Approved reason must be kebab-case identifier (got: %s)", reasonLit.Value), true
	}
	// Rule 6: reason must not be a placeholder identifier.
	if panicRegisteredReasonPlaceholder.MatchString(reasonVal) {
		return fmt.Sprintf(
			"reason %q is a placeholder identifier (todo/fixme/tbd/xxx/placeholder/wip);"+
				" replace with descriptive kebab-case",
			reasonVal,
		), true
	}
	return "", false
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
// scanned by PANIC-REGISTERED-01 (test files, generated code, testdata, etc.).
// Mirrors fileroles.IsProductionCode exclusions for paths that RunTyped may
// surface but the rule should not gate. All prefixes are module-relative, so
// the skip set is correct for any module under analysis (an external repo
// simply will not have a "tools/archtest/" tree, in which case that arm never
// matches — harmless).
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
	case strings.Contains(rel, "/auditcoretest/"):
		return true
	}
	return false
}

// CheckPanicRegistered runs PANIC-REGISTERED-01 over the running module and
// returns its diagnostics. It is the importable [CellRule] body wrapped by
// [StandardCellRules]; GoCell's TestPanicRegistered calls it directly so the
// gate has a single source. cfg is currently unused (the scan is module-wide,
// resolved from the running module's go.mod by RunTyped) and is accepted to
// satisfy the uniform CellRule.Run signature.
//
// Two RunTyped loads cover the full build-directive file set (default + the
// FlatNonDefaultTags union), deduped by "rel:line:reason"; see the long note
// in the prior TestPanicRegistered body / ADR 202605190000 §Alternatives for
// why a single union load is insufficient.
func CheckPanicRegistered(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	seen := make(map[string]struct{}) // dedup across two loads by "rel:line:reason"
	var violations []panicRegisteredViolation
	scan := func(p *Pass) []Diagnostic {
		violations = append(violations, collectPanicViolations(p, seen)...)
		return nil
	}

	_ = RunTyped(t, TypedOpts{}, []string{"./..."}, scan)
	_ = RunTyped(t, TypedOpts{Tags: FlatNonDefaultTags()}, []string{"./..."}, scan)

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})

	out := make([]Diagnostic, 0, len(violations))
	for _, v := range violations {
		out = append(out, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
	}
	return out
}

// collectPanicViolations scans one typed Pass for PANIC-REGISTERED-01
// violations, applying the file-level skip set and deduping against seen
// (keyed "rel:line:reason") so the two-load union does not double-count.
// Returns nil when the Pass lacks type info.
func collectPanicViolations(p *Pass, seen map[string]struct{}) []panicRegisteredViolation {
	if p.TypesInfo == nil || p.Fset == nil {
		return nil
	}
	var out []panicRegisteredViolation
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
			out = append(out, v)
		}
	}
	return out
}
