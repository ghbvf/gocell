package archtest

// invariants:
//   - INVARIANT: PANIC-REDACT-01
//   - INVARIANT: PANIC-REGISTERED-01
//
// panic_invariants_test.go — test entry points for panic-related invariants.
//
//	PANIC-REDACT-01     slog.Any("panic", X) must wrap X with redaction.RedactAny(...)
//	PANIC-REGISTERED-01 every production panic() call must wrap its argument with
//	                    panicregister.Approved(reason, value). The rule logic and
//	                    the importable CheckPanicRegistered entry live in
//	                    panic_invariants.go (so an external Cell repo can run it
//	                    via StandardCellRules); this file dogfoods that shared
//	                    logic against GoCell itself — single source, no parallel
//	                    rule body. See pkg/panicregister and
//	                    docs/architecture/202604270030-architectural-panic-whitelist.md.

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"testing"
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
// TestPanicRegistered dogfoods PANIC-REGISTERED-01 against GoCell itself by
// calling the same CheckPanicRegistered that StandardCellRules (and external
// Cell repos via RunStandardCellRules) use — single source, no parallel rule
// body. The scanner logic lives in panic_invariants.go.
//
// NOTE: All call-site migrations complete as of PR #467; this test must pass.
func TestPanicRegistered(t *testing.T) {
	t.Parallel()
	Report(t, rulePanicRegistered01, CheckPanicRegistered(t, ConfigForExternalCell{}))
}

// TestPanicRegisteredScannerFixtures verifies the PANIC-REGISTERED-01 rule
// logic against static fixture packages under
// tools/archtest/testdata/panic_registered_fixtures/.
func TestPanicRegisteredScannerFixtures(t *testing.T) {
	t.Parallel()

	// Each fixture dir owns a diag.golden capturing the rule's real output
	// (Rel:Line: Message). GREEN fixtures have an empty golden. Line numbers
	// live in the regenerated golden, never in this table — adding an import
	// to a fixture and re-running with -update produces a clean positional
	// delta, not a false failure. See ADR
	// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
	dirs := []string{
		// RED cases — expect violations.
		"bare_string_red", "non_funnel_err_red", "non_literal_reason_red",
		"old_errcode_form_red", "must_prefix_bare_red",
		// GREEN cases — expect 0 violations (empty golden).
		"assertion_wrapped_green", "recovered_value_green", "must_prefix_wrapped_green",
		// RED cases for reason argument shape.
		"reason_const_ident_red", "reason_format_invalid_red",
		// RED/GREEN cases for payload type guard (RC-C1).
		"payload_type_invalid_red", "payload_type_valid_green",
		// RED cases for reason placeholder denylist (RC-B1).
		"reason_placeholder_red",
	}

	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()

			fixturePattern := "./tools/archtest/testdata/panic_registered_fixtures/" + dir

			var diags []Diagnostic
			// Load using module root so imports of panicregister/errcode resolve.
			_ = RunTyped(t, TypedOpts{}, []string{fixturePattern}, func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil || p.Fset == nil {
					return nil
				}
				for _, file := range p.Files {
					rel := p.Rel(file)
					for _, v := range scanFileForPanicViolations(p.Fset, file, p.TypesInfo, rel) {
						diags = append(diags, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
					}
				}
				return nil
			})

			goldenPath := filepath.Join(root, "tools", "archtest", "testdata",
				"panic_registered_fixtures", dir, "diag.golden")
			AssertGolden(t, goldenPath, diags)
		})
	}
}
