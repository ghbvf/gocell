package archtest

// INVARIANT: PANIC-REGISTERED-01
//
// panic_invariants_test.go — test entry points for panic-related invariants.
//
//	PANIC-REGISTERED-01 every production panic() call must wrap its argument with
//	                    panicregister.Approved(reason, value). The rule logic and
//	                    the importable CheckPanicRegistered entry live in
//	                    panic_invariants.go (so an external Cell repo can run it
//	                    via StandardCellRules); this file dogfoods that shared
//	                    logic against GoCell itself — single source, no parallel
//	                    rule body. See pkg/panicregister and
//	                    docs/architecture/202604270030-architectural-panic-whitelist.md.
//
// Note: PANIC-REDACT-01 (slog.Any("panic", X) must wrap X with redaction.RedactAny)
// was retired in PR #1036 Batch 2. slog sink-side redaction (SLOG-HANDLER-SEALED-FUNNEL-01,
// tools/archtest/slog_handler_sealed_funnel_test.go) now provides fail-closed
// value redaction at the slog.Handler level for all log output, superseding the
// call-site Soft archtest. The call-site redaction.RedactAny calls are preserved
// as defense-in-depth but are no longer enforced by archtest.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"testing"
)

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
	// GoCell's production files behind //go:build directives are gated by its
	// full non-default tag union; pass it so the second scan pass covers them.
	Report(t, rulePanicRegistered01,
		CheckPanicRegistered(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestScanPanicBuiltinShadows is the reverse self-check for the one declared
// blind spot of the pure-AST panic detection (see scanPanicBuiltinShadows
// godoc): the rule matches the `panic` builtin by name, so a declaration that
// shadows it must be flagged. The other conceivable evasion — aliasing the
// builtin as a value (`p := panic; p(x)`) — is a Go compile error (builtins are
// not values), so it is structurally impossible and absent here by design. This
// test proves the shadow closure fires (RED cases) and does not false-flag a
// normal panic(...) call (GREEN case). The whole-module guarantee that GoCell's
// production tree contains zero shadows is enforced by TestPanicRegistered,
// which now also runs scanPanicBuiltinShadows over every production file.
func TestScanPanicBuiltinShadows(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		src      string
		wantViol bool
	}{
		{"normal-call", "package p\nfunc f() { panic(\"x\") }\n", false},
		{"func-shadow", "package p\nfunc panic(any) {}\nfunc f() { panic(1) }\n", true},
		{"var-shadow", "package p\nfunc f() {\n\tpanic := func(any) {}\n\tpanic(1)\n}\n", true},
		{"param-shadow", "package p\nfunc f(panic func(any)) { panic(1) }\n", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, c.name+".go", c.src, 0)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			info := &types.Info{
				Defs: map[*ast.Ident]types.Object{},
				Uses: map[*ast.Ident]types.Object{},
			}
			conf := types.Config{Error: func(error) {}} // tolerate intentional shadows
			_, _ = conf.Check("p", fset, []*ast.File{file}, info)
			if got := len(scanPanicBuiltinShadows(fset, file, info, c.name)) > 0; got != c.wantViol {
				t.Errorf("scanPanicBuiltinShadows(%s) flagged=%v, want=%v", c.name, got, c.wantViol)
			}
		})
	}
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
