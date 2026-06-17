//go:build archtest

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

	"github.com/ghbvf/gocell/tools/internal/fileroles"
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
	rule := func(p *Pass) []Diagnostic {
		var ds []Diagnostic
		for _, f := range p.Files {
			ds = append(ds, panicLogRedactViolations(p, f, filepath.ToSlash(p.Rel(f)))...)
		}
		return ds
	}
	// Two passes: default build tags PLUS the project's full non-default tag union
	// (FlatNonDefaultTags), so tag-gated production files (`//go:build integration`
	// etc.) that recover-and-log a panic are also scanned — same coverage discipline
	// as the sibling PANIC-REGISTERED-01. A single default-tag pass would silently
	// skip those files. Dedup by rel:line:message (a file may appear in both passes).
	seen := make(map[string]bool)
	var all []Diagnostic
	both := append(
		Run(t, Production(TypedOpts{Tests: false}), rule),
		Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}), rule)...,
	)
	for _, d := range both {
		key := d.Rel + ":" + strconv.Itoa(d.Line) + ":" + d.Message
		if !seen[key] {
			seen[key] = true
			all = append(all, d)
		}
	}
	Report(t, "PANIC-LOG-REDACT-01", all)
}

// TestPanicLogRedact_DetectsViolation is the reverse self-check: it runs the
// detector against a real fixture package (loaded type-checked via
// Run(t, Fixture(...))) that contains one compliant call plus two violations (a bare
// value and a non-RedactAny wrapper) — asserting exactly the two violations are
// flagged. This proves the typed form-lock is non-vacuous.
func TestPanicLogRedact_DetectsViolation(t *testing.T) {
	t.Parallel()

	const fixturePkgPath = PlatformModulePath + "/tools/archtest/testdata/panic_log_redact_fixtures/violation"

	diags := Run(t, Fixture(FixtureOpts{Tests: false},
		[]string{"./tools/archtest/testdata/panic_log_redact_fixtures/violation"}),
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

// TestPanicRegisteredUsesProductionScope locks #2148's vacuity fix:
// CheckPanicRegistered must scan via Production(...) — the workspace-aware scope
// that expands to one ./<dir>/... per go.work member (see
// typeseval.LoadProductionPackages) — never Typed(./...), which at GoCell's
// module-less workspace root resolves to ZERO packages, leaving the entire
// production tree silently unscanned. Its sibling TestPanicLogRedact already
// uses Production(); this converges both panic gates onto one scope.
//
// AI-robust: Medium (type-aware AST scan; a regression to Typed(./...) re-vacates
// the dogfood scan and fails here). Mirrors
// TestClockChecksDoNotUseProdscanPatternsExtended. Production() subsumes the
// external single-module cell via workspace.Modules' single-module fallback, so
// no transport-specific dual-path is needed.
//
// Residual (Soft, accepted): matches the bare-Ident call form `Production(...)`
// (same-package direct call). A migration to a qualified form
// (`archtest.Production(...)`, a SelectorExpr) would slip past this scan — update
// the matcher then. The non-vacuous coverage itself is held by the companion
// TestPanicRegisteredScopeIncludesSatellites.
func TestPanicRegisteredUsesProductionScope(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	path := filepath.Join(root, "tools", "archtest", "panic_invariants.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse panic_invariants.go: %v", err)
	}
	var found, sawProduction bool
	EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Name == nil || fn.Body == nil || fn.Name.Name != "CheckPanicRegistered" {
			return
		}
		found = true
		EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return
			}
			switch ident.Name {
			case "Production":
				sawProduction = true
			case "Typed":
				t.Errorf("CheckPanicRegistered must use Production(...) scope, not Typed(./...); " +
					"Typed drops go.work satellites and is vacuous at the module-less workspace root (#2148)")
			}
		})
	})
	if !found {
		t.Errorf("CheckPanicRegistered not found in panic_invariants.go (renamed?); " +
			"update TestPanicRegisteredUsesProductionScope")
	}
	if !sawProduction {
		t.Errorf("CheckPanicRegistered must call Production(...) so PANIC-REGISTERED-01 scans " +
			"the whole workspace including satellites (#2148)")
	}
}

// TestPanicRegisteredScopeIncludesSatellites is the self-contained anti-vacuity
// companion: it proves the Production() scope that CheckPanicRegistered now uses
// actually visits satellite-module production files, so PANIC-REGISTERED-01
// coverage is non-vacuous after #2148. Mirrors
// TestClockWorkspaceScopeIncludesSatellites.
func TestPanicRegisteredScopeIncludesSatellites(t *testing.T) {
	t.Parallel()
	// examples/iotdevice/run.go proves the Production() scope LOADS an examples
	// production file (the FILTER side — that shouldSkipForPanicRegistered does
	// not drop it again — is held by TestPanicRegisteredDoesNotSkipExamples,
	// #2149); it is also that test's disk-existence no-stale anchor (rename/delete
	// fails here).
	assertScopeVisits(t, "PANIC-REGISTERED-01 Production", Production(TypedOpts{Tests: false}),
		"cmd/gocell/main.go", "cmd/corebundle/main.go", "examples/iotdevice/run.go")
}

// TestPanicRegisteredDoesNotSkipExamples is the FILTER-stage companion to
// TestPanicRegisteredScopeIncludesSatellites: loading an examples file into the
// scan is necessary but not sufficient — shouldSkipForPanicRegistered must also
// not drop it again. Codex's PR #2252 review (cluster C1) caught exactly that
// drift: #2149 put examples/ under production governance and CheckPanicRegistered
// scans it via Production(), but the rule's own file filter still skipped the
// whole examples/ tree, so PANIC-REGISTERED-01 over examples was false-green.
//
// This binds the filter's examples treatment to the single source
// fileroles.IsProductionCode (which returns true for examples/): the positive
// fixture must be BOTH unskipped here AND production per fileroles, so the two
// classifiers cannot drift apart on examples/ again. The fixture's disk
// existence is held by TestPanicRegisteredScopeIncludesSatellites (same path),
// so no extra stat is needed here.
//
// AI-robust: Medium (runtime guard; re-adding the examples skip turns this RED).
// A Hard form — deriving the skip set from fileroles so a separate examples arm
// is unexpressible — needs a consumer-supplied SkipPaths seam for the importable
// external-cell rule; that full single-sourcing is tracked in #1302.
func TestPanicRegisteredDoesNotSkipExamples(t *testing.T) {
	t.Parallel()
	const examplesProd = "examples/iotdevice/run.go"
	if shouldSkipForPanicRegistered(examplesProd) {
		t.Errorf("shouldSkipForPanicRegistered(%q) = true; examples/ is production code "+
			"and must be scanned by PANIC-REGISTERED-01 (#2149/#2252 C1)", examplesProd)
	}
	if !fileroles.IsProductionCode(examplesProd) {
		t.Errorf("fileroles.IsProductionCode(%q) = false; the examples production "+
			"classification drifted from the single source — re-sync the filter", examplesProd)
	}
	// Anti-vacuity: the skip set must still drop genuine non-production paths,
	// so the examples-arm removal did not blunt the whole filter.
	for _, rel := range []string{"examples/iotdevice/run_test.go", "vendor/x/y.go", "generated/z.go"} {
		if !shouldSkipForPanicRegistered(rel) {
			t.Errorf("shouldSkipForPanicRegistered(%q) = false; the skip set is broken", rel)
		}
	}
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
			_ = Run(t, Typed(TypedOpts{}, []string{fixturePattern}), func(p *Pass) []Diagnostic {
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
