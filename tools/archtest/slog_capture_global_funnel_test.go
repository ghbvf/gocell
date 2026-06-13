//go:build archtest

package archtest

// INVARIANT: SLOG-CAPTURE-GLOBAL-FUNNEL-01
//
// slog_capture_global_funnel_test.go — Medium caller-allowlist funnel for the
// global-mutating test helper healthtest.NewCapture, paired with the
// de-globalized alternative healthtest.NewLoggerCapture.
//
// Why this rule exists (#1490): healthtest.NewCapture(t) rewrites the
// process-global slog.Default() (and restores via t.Cleanup). When a
// component under test emits a log from an ASYNC goroutine (e.g. eventbus's
// subscription/retry goroutine) AND a parallel sibling test in the same test
// binary calls NewCapture/Cleanup in between the wait and the snapshot, the
// async record routes to the WRONG handler — the classic "global mutable
// state + async write + parallel tests" race that made
// runtime/eventbus.TestNotifyRetryExhausted flaky in CI.
//
// The fix de-globalizes eventbus (inject *slog.Logger via WithLogger) and the
// test captures via healthtest.NewLoggerCapture() — which builds a CaptureHandler
// WITHOUT touching slog.Default(). To stop the flake CLASS from reappearing in
// new component tests (a Soft naming/doc convention is forbidden by
// .claude/rules/gocell/ai-robust.md "Soft 严禁立项"), this funnel makes the
// misuse machine-detectable:
//
//   - Banned surface: healthtest.NewCapture (the only function that calls
//     slog.SetDefault in test scope).
//   - Sanctioned callers (allowlist): runtime/bootstrap, cmd/corebundle, and
//     healthtest's own package test. These are independent test binaries
//     (process-level slog state — eventbus's parallel race cannot reach across
//     processes), capture SYNCHRONOUS logs (readyz handler / run()), and
//     legitimately verify that the bootstrapped system emits via slog.Default()
//     (the SLOG-HANDLER-SEALED-FUNNEL-01 A3 SetDefault wiring). The allowlist
//     is a shrink-only migration ledger.
//   - Everyone else MUST use healthtest.NewLoggerCapture() + a WithLogger-style
//     injection so async log capture stays isolated from parallel siblings.
//
// Funnel closure (per ai-robust.md "只锁 callsite 不是闭环 funnel"):
//   - Direct qualified call (healthtest.NewCapture(...)) → TestSlogCaptureGlobalFunnel.
//   - Indirect reference (var f = healthtest.NewCapture, pointer pass-through,
//     struct-field binding) → TestSlogCaptureGlobalFunnel_NoIndirectReferences.
//   Together they make the surface unreachable from disallowed packages by any
//   syntactic shape.
//
// AI-robust grading: Medium. The banned-symbol identity is resolved by go/types
// (info.Uses, alias-proof) — Hard for "did code reach this symbol". But the axis
// "WHICH packages may call a given function" is not expressible in the Go type
// system; no type-level gate can forbid an arbitrary package from importing
// healthtest and calling NewCapture. That permanent Go-language ceiling is the
// same shape as #1352 / #851 / #893 (archtest-bound, not compile-bound).
//
// Blind spots of the chosen tool (per ai-robust.md "工具选定后强制盲区自检"):
//   - Same-package unqualified call: a bare NewCapture(...) inside package
//     healthtest is an *ast.Ident (not *ast.SelectorExpr) and is NOT scanned.
//     The only such caller is healthtest's own slogcapture_test.go — already in
//     the allowlist dir, so this is closed by construction.
//   - Reflect call: reflect.ValueOf(healthtest.NewCapture).Call(...). The
//     reflect ValueOf argument is an indirect reference caught by the reverse
//     self-test; the .Call dispatch itself is not modeled (accepted: a test
//     reflecting over a capture helper to evade this rule is implausible and
//     review-visible).
//   - Rename: if NewCapture is renamed, the rule silently stops guarding it —
//     closed by TestSlogCaptureGlobalFunnel_SymbolSentinel.
//
// See runtime/http/health/healthtest/slogcapture.go godoc and
// tools/archtest/test_eventually_funnel_test.go (the sibling test-helper
// funnel range this rule is modeled on).

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

const ruleSlogCaptureGlobalFunnel01 = "SLOG-CAPTURE-GLOBAL-FUNNEL-01"

const newCaptureFuncName = "NewCapture"

// healthtestPkgPath is derived from PlatformModulePath (not a bare literal) so a
// module rename / /v2 bump updates exactly one place (ARCHTEST-MODULE-PATH-FUNNEL-01).
var healthtestPkgPath = PlatformModulePath + "/runtime/http/health/healthtest"

// slogCaptureAllowlist is the closed, shrink-only set of rel-path dir prefixes
// whose tests may call the global-mutating healthtest.NewCapture. See the file
// godoc for why each is sanctioned.
var slogCaptureAllowlist = []string{
	"runtime/bootstrap/",
	"cmd/corebundle/",
	"runtime/http/health/healthtest/",
}

const slogCaptureFunnelReason = "healthtest.NewCapture mutates global slog.Default() " +
	"(races parallel siblings under async log writes, #1490); only {runtime/bootstrap, " +
	"cmd/corebundle, runtime/http/health/healthtest} may call it — component tests use " +
	"healthtest.NewLoggerCapture() + WithLogger injection"

const slogCaptureIndirectReason = "indirect reference to healthtest.NewCapture (function " +
	"value / pointer pass-through / struct-field binding); it mutates global slog.Default() " +
	"(#1490) — capture via healthtest.NewLoggerCapture() at the callsite instead"

type slogCaptureFunnelViolation struct {
	File   string
	Line   int
	Reason string
}

// isSlogCaptureAllowlistedCaller reports whether rel lives under a sanctioned
// caller directory (allowlist membership is by file path, robust to test-package
// path naming quirks like the xtest "_test" suffix).
func isSlogCaptureAllowlistedCaller(rel string) bool {
	for _, p := range slogCaptureAllowlist {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	return false
}

// shouldSkipForSlogCaptureFunnel excludes generated/vendored/fixture/out-of-tree
// paths from the module-wide scan. Mirrors shouldSkipForEventuallyFunnel. The
// fixture loop does NOT call this (it must see testdata fixtures).
func shouldSkipForSlogCaptureFunnel(rel string) bool {
	switch {
	case strings.HasPrefix(rel, "vendor/"),
		strings.HasPrefix(rel, "generated/"),
		strings.HasPrefix(rel, "tools/archtest/testdata/"),
		strings.HasPrefix(rel, "worktrees/"),
		strings.HasPrefix(rel, ".git/"),
		strings.HasPrefix(rel, "node_modules/"),
		strings.HasPrefix(rel, "testdata/"),
		strings.Contains(rel, "/testdata/"):
		return true
	}
	return false
}

// isHealthtestNewCaptureFunc reports whether obj is the healthtest.NewCapture
// function. Single membership check shared by the direct-call and indirect-ref
// scanners (keeps "what counts as banned" single-sourced).
func isHealthtestNewCaptureFunc(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == healthtestPkgPath && fn.Name() == newCaptureFuncName
}

// isHealthtestNewCaptureCallee reports whether funExpr is a qualified call to
// healthtest.NewCapture. Resolution is via info.Uses (alias-proof). When info is
// nil (never in Typed/Fixture mode, but defensive) falls back to pure-AST
// pkg-ident "healthtest".
func isHealthtestNewCaptureCallee(funExpr ast.Expr, info *types.Info) bool {
	sel, ok := funExpr.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != newCaptureFuncName {
		return false
	}
	if info != nil {
		return isHealthtestNewCaptureFunc(info.Uses[sel.Sel])
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "healthtest"
}

// scanFileForSlogCaptureFunnelViolations returns direct-call violations of
// SLOG-CAPTURE-GLOBAL-FUNNEL-01 in one file. Allowlisted-caller files yield
// none. Shared by the module-wide test and the fixture test.
func scanFileForSlogCaptureFunnelViolations(
	fset *token.FileSet, file *ast.File, info *types.Info, rel string,
) []slogCaptureFunnelViolation {
	if isSlogCaptureAllowlistedCaller(rel) {
		return nil
	}
	var violations []slogCaptureFunnelViolation
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isHealthtestNewCaptureCallee(call.Fun, info) {
			return
		}
		violations = append(violations, slogCaptureFunnelViolation{
			File:   rel,
			Line:   fset.Position(call.Pos()).Line,
			Reason: slogCaptureFunnelReason,
		})
	})
	return violations
}

// TestSlogCaptureGlobalFunnel enforces SLOG-CAPTURE-GLOBAL-FUNNEL-01 module-wide
// over all test sources (Tests:true — NewCapture is a test-only API). Two loads
// (default + flat non-default tags) union build-tagged files. The current tree
// is GREEN: eventbus migrated to NewLoggerCapture; remaining callers are all
// allowlisted (bootstrap / corebundle / healthtest self-test).
func TestSlogCaptureGlobalFunnel(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []slogCaptureFunnelViolation

	scan := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if shouldSkipForSlogCaptureFunnel(rel) {
				continue
			}
			for _, v := range scanFileForSlogCaptureFunnelViolations(p.Fset, file, p.TypesInfo, rel) {
				key := fmt.Sprintf("%s:%d", v.File, v.Line)
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

	sortSlogCaptureViolations(violations)
	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleSlogCaptureGlobalFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s: healthtest.NewCapture (global slog.SetDefault) may only be called from "+
			"{runtime/bootstrap, cmd/corebundle, runtime/http/health/healthtest}; component "+
			"tests must capture via healthtest.NewLoggerCapture() + WithLogger injection "+
			"(avoids the async/parallel race #1490). "+
			"Run: go test -tags=archtest -count=1 ./tools/archtest/... -run TestSlogCaptureGlobalFunnel$ for local repro.",
		ruleSlogCaptureGlobalFunnel01)
}

// TestSlogCaptureGlobalFunnelFixtures verifies the direct-call rule against
// static fixtures. positive_green captures via CaptureHandler directly (the
// pattern NewLoggerCapture wraps) → empty golden; disallowed_caller_red calls
// healthtest.NewCapture from a non-allowlisted package → one violation.
//
// Regenerate goldens: go test -tags=archtest ./tools/archtest -run TestSlogCaptureGlobalFunnelFixtures$ -update.
func TestSlogCaptureGlobalFunnelFixtures(t *testing.T) {
	t.Parallel()

	dirs := []string{
		"positive_green",
		"disallowed_caller_red",
	}
	root := findModuleRoot(t)
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/slog_capture_funnel_fixtures/" + dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil || p.Fset == nil {
					return nil
				}
				var out []Diagnostic
				for _, file := range p.Files {
					rel := p.Rel(file)
					for _, v := range scanFileForSlogCaptureFunnelViolations(p.Fset, file, p.TypesInfo, rel) {
						out = append(out, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
					}
				}
				return out
			})
			golden := filepath.Join(root, "tools", "archtest", "testdata",
				"slog_capture_funnel_fixtures", dir, "diag.golden")
			AssertGolden(t, golden, diags)
		})
	}
}

// collectDirectNewCaptureCallIdents returns the SelectorExpr.Sel idents whose
// callee is healthtest.NewCapture (direct calls). The reverse self-test excludes
// these so direct calls (caught by the main rule) aren't double-reported.
func collectDirectNewCaptureCallIdents(file *ast.File, info *types.Info) map[*ast.Ident]struct{} {
	out := make(map[*ast.Ident]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil {
			return
		}
		if isHealthtestNewCaptureCallee(call.Fun, info) {
			out[sel.Sel] = struct{}{}
		}
	})
	return out
}

// scanFileForIndirectNewCaptureReferences returns references to
// healthtest.NewCapture that are NOT direct calls (function value, pointer
// pass-through, struct-field binding) — the indirect-shape bypass of the
// CallExpr-driven main rule. Allowlisted-caller files yield none.
func scanFileForIndirectNewCaptureReferences(
	p *Pass, file *ast.File, rel string,
) []slogCaptureFunnelViolation {
	if isSlogCaptureAllowlistedCaller(rel) {
		return nil
	}
	directCall := collectDirectNewCaptureCallIdents(file, p.TypesInfo)
	absFile := p.Abs(file)
	seenIdent := make(map[*ast.Ident]struct{})
	var out []slogCaptureFunnelViolation

	for ident, obj := range p.TypesInfo.Uses {
		if ident == nil || obj == nil {
			continue
		}
		if !isHealthtestNewCaptureFunc(obj) {
			continue
		}
		if p.Fset.Position(ident.Pos()).Filename != absFile {
			continue
		}
		if _, isDirect := directCall[ident]; isDirect {
			continue
		}
		if _, dup := seenIdent[ident]; dup {
			continue
		}
		seenIdent[ident] = struct{}{}
		out = append(out, slogCaptureFunnelViolation{
			File:   rel,
			Line:   p.Fset.Position(ident.Pos()).Line,
			Reason: slogCaptureIndirectReason,
		})
	}
	return out
}

// TestSlogCaptureGlobalFunnel_NoIndirectReferences is the blind-spot reverse
// self-test (ai-robust.md "工具选定后强制盲区自检"): healthtest.NewCapture must
// not be reachable from disallowed packages via indirect references either.
// Together with the main rule this closes the funnel ("只锁 callsite 不是闭环 funnel").
func TestSlogCaptureGlobalFunnel_NoIndirectReferences(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{})
	var violations []slogCaptureFunnelViolation

	scan := func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if shouldSkipForSlogCaptureFunnel(rel) {
				continue
			}
			for _, v := range scanFileForIndirectNewCaptureReferences(p, file, rel) {
				key := fmt.Sprintf("%s:%d", v.File, v.Line)
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

	sortSlogCaptureViolations(violations)
	if len(violations) > 0 {
		t.Logf("%s blind-spot self-test: %d indirect reference(s):", ruleSlogCaptureGlobalFunnel01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d — %s", v.File, v.Line, v.Reason)
		}
	}
	assert.Empty(t, violations,
		"%s blind-spot reverse self-test: healthtest.NewCapture must not be reachable via "+
			"indirect references (function value, pointer pass-through, struct field) from "+
			"disallowed packages; capture via healthtest.NewLoggerCapture(). "+
			"Run: go test -tags=archtest -count=1 ./tools/archtest/... -run TestSlogCaptureGlobalFunnel_NoIndirectReferences$ for local repro.",
		ruleSlogCaptureGlobalFunnel01)
}

// TestSlogCaptureGlobalFunnel_NoIndirectReferences_Fixtures proves the indirect
// scanner is wired (anti-vacuity): indirect_ref_red binds healthtest.NewCapture
// to a function-value var → one violation. Without this RED fixture the
// module-wide reverse test is always green (current tree has no indirect refs).
func TestSlogCaptureGlobalFunnel_NoIndirectReferences_Fixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	dirs := []string{"indirect_ref_red"}
	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			pattern := "./tools/archtest/testdata/slog_capture_funnel_fixtures/" + dir
			diags := Run(t, Fixture(FixtureOpts{}, []string{pattern}), func(p *Pass) []Diagnostic {
				if p.TypesInfo == nil || p.Fset == nil {
					return nil
				}
				var out []Diagnostic
				for _, file := range p.Files {
					rel := p.Rel(file)
					for _, v := range scanFileForIndirectNewCaptureReferences(p, file, rel) {
						out = append(out, Diagnostic{Rel: v.File, Line: v.Line, Message: v.Reason})
					}
				}
				return out
			})
			golden := filepath.Join(root, "tools", "archtest", "testdata",
				"slog_capture_funnel_fixtures", dir, "diag.golden")
			AssertGolden(t, golden, diags)
		})
	}
}

// TestSlogCaptureGlobalFunnel_SymbolSentinel locks the banned-symbol identity
// against rename/removal drift: if healthtest.NewCapture is renamed, the
// SelectorExpr-name predicate silently stops firing and the funnel becomes a
// no-op. This sentinel walks the loaded healthtest package scope and fails if
// NewCapture is no longer a function there — pointing the reviewer here to update
// newCaptureFuncName alongside any rename.
func TestSlogCaptureGlobalFunnel_SymbolSentinel(t *testing.T) {
	t.Parallel()

	found := false
	scan := func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, imp := range p.Pkg.Imports() {
			if imp.Path() != healthtestPkgPath {
				continue
			}
			if obj := imp.Scope().Lookup(newCaptureFuncName); obj != nil {
				if _, ok := obj.(*types.Func); ok {
					found = true
				}
			}
		}
		return nil
	}

	_ = Run(t, Typed(TypedOpts{Tests: true}, []string{"./..."}), scan)

	assert.True(t, found,
		"%s sentinel: healthtest.%s not found in any loaded scope — renamed/removed? "+
			"This funnel silently stops guarding the global-slog test helper. Update "+
			"newCaptureFuncName AND verify the SelectorExpr predicate covers the new name.",
		ruleSlogCaptureGlobalFunnel01, newCaptureFuncName)
}

func sortSlogCaptureViolations(v []slogCaptureFunnelViolation) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].File != v[j].File {
			return v[i].File < v[j].File
		}
		return v[i].Line < v[j].Line
	})
}
