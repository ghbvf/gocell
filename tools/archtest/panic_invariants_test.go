package archtest

// invariants:
//   - INVARIANT: PANIC-REGISTERED-01
//   - INVARIANT: PANIC-LOG-REDACT-01
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
//	PANIC-LOG-REDACT-01 every production slog.Any("panic", X) must have
//	                    X = redaction.RedactAny(...).
//
// PANIC-LOG-REDACT-01 was retired as a Soft string-anchor check in PR #1036
// Batch 2 (sink-side redaction superseded its value-redaction role). It is
// RESTORED here (#1432) as a Medium TYPED form-lock — NOT the old Soft form: the
// callee (log/slog.Any) and the value wrapper (pkg/redaction.RedactAny) are both
// resolved via go/types (IsCallToPkgFunc), and the "panic" key literal is only
// the locator. This keeps the call-site defense-in-depth from silently
// regressing across all production recovery sites, which matters because panic
// logging runs in recovery paths that may execute in contexts where the
// process-global slog seal is not active (library/test code).
//
// Residual (accepted): a panic value logged under a non-literal key, or via a
// different slog constructor than slog.Any, is not matched — the same locator
// limitation as the retired rule, but the form within the "panic"-keyed slog.Any
// set is now typed-locked rather than string-anchored.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// panicLogRedactViol is the PANIC-LOG-REDACT-01 diagnostic.
const panicLogRedactViol = `slog.Any("panic", X) must wrap X with redaction.RedactAny(X)` +
	` — call-site defense-in-depth must not regress (PANIC-LOG-REDACT-01)`

// panicLogRedactViolations is the PANIC-LOG-REDACT-01 detector: every
// slog.Any("panic", X) call (callee resolved via go/types) must have X be a
// redaction.RedactAny(...) call (also go/types-resolved). Shared by the
// production scan and the reverse self-check fixture.
func panicLogRedactViolations(p *Pass, f *ast.File, rel string) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	var ds []Diagnostic
	EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		if !IsCallToPkgFunc(p.TypesInfo, call, slogFunnelStdlibPkgPath, "Any") {
			return
		}
		if len(call.Args) != 2 {
			return
		}
		bl, ok := call.Args[0].(*ast.BasicLit)
		if !ok || bl.Kind != token.STRING {
			return
		}
		key, err := strconv.Unquote(bl.Value)
		if err != nil || key != "panic" {
			return
		}
		if valCall, ok := call.Args[1].(*ast.CallExpr); ok &&
			IsCallToPkgFunc(p.TypesInfo, valCall, redactionPkgPath, "RedactAny") {
			return // compliant
		}
		pos := p.Fset.Position(call.Pos())
		ds = append(ds, Diagnostic{Rel: rel, Line: pos.Line, Message: panicLogRedactViol})
	})
	return ds
}

// INVARIANT: PANIC-LOG-REDACT-01
//
// TestPanicLogRedact enforces PANIC-LOG-REDACT-01 across the production tree:
// every slog.Any("panic", X) must have X = redaction.RedactAny(...). AI-robust
// rating: Medium (typed (callee, arg) form-lock; the "panic" key literal is the
// locator, the RedactAny wrapper is the typed lock). See file-header note.
func TestPanicLogRedact(t *testing.T) {
	t.Parallel()
	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			ds = append(ds, panicLogRedactViolations(p, f, filepath.ToSlash(p.Rel(f)))...)
		}
		return ds
	})
	Report(t, "PANIC-LOG-REDACT-01", diags)
}

// TestPanicLogRedact_DetectsViolation is the reverse self-check: it runs the
// detector against a real fixture package (loaded type-checked via
// RunTypedFixture) that contains one compliant call plus two violations (a bare
// value and a non-RedactAny wrapper) — asserting exactly the two violations are
// flagged. This proves the typed form-lock is non-vacuous.
func TestPanicLogRedact_DetectsViolation(t *testing.T) {
	t.Parallel()

	const fixturePkgPath = "github.com/ghbvf/gocell/tools/archtest/testdata/panic_log_redact_fixtures/violation"

	diags := RunTypedFixture(t, FixtureOpts{Tests: false},
		[]string{"./tools/archtest/testdata/panic_log_redact_fixtures/violation"},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != fixturePkgPath {
				return nil
			}
			var ds []Diagnostic
			for _, f := range p.Files {
				ds = append(ds, panicLogRedactViolations(p, f, filepath.ToSlash(p.Rel(f)))...)
			}
			return ds
		})

	require.Len(t, diags, 2,
		"PANIC-LOG-REDACT-01 must flag exactly 2 violations (bare value + non-RedactAny"+
			" wrapper); the RedactAny-wrapped call must NOT be flagged; got: %v", diags)
	for _, d := range diags {
		assert.Contains(t, d.Message, "PANIC-LOG-REDACT-01")
	}
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
