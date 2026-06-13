//go:build archtest

package archtest

// INVARIANT: SLOG-CAPTURE-GLOBAL-FUNNEL-01
//
// slog_capture_global_funnel_test.go — Medium caller-allowlist funnel that seals
// the test-scope process-global slog mutation PRIMITIVE: raw slog.SetDefault.
//
// Why this rule exists (#1490): a component that emits a log from an ASYNC
// goroutine (e.g. eventbus's subscription/retry goroutine) while a parallel
// sibling test rewrites the process-global slog.Default() between the wait and
// the snapshot routes its async record to the WRONG handler — the classic
// "global mutable state + async write + parallel tests" race that made
// runtime/eventbus.TestNotifyRetryExhausted flaky in CI.
//
// The first cut of this rule banned ONE wrapper of that primitive
// (healthtest.NewCapture). Review (#2003 F1) showed the dangerous AXIS is the
// raw primitive slog.SetDefault itself — banning the wrapper left the primitive
// open, so the flake class could be re-expressed by any test calling
// slog.SetDefault inline. This rule now seals the primitive:
//
//   - Banned surface: a call to stdlib log/slog.SetDefault from any test-scope
//     code outside the sanctioned holder.
//   - Single sanctioned holder (allowlist): pkg/testutil/slogcapture — its
//     InstallDefault(t, *slog.Logger) is the ONE place that calls slog.SetDefault
//     (+ t.Cleanup restore). Lives in pkg/ so every layer can import it; stdlib
//     only, so it trips neither SLOG-HANDLER-SEALED-FUNNEL-01 A1 nor that rule's
//     Handler-impl blind spot.
//   - Everyone else redirects the default via slogcapture.InstallDefault (or
//     healthtest.NewCapture, which delegates to it). A test of a component that
//     accepts an injected logger uses healthtest.NewLoggerCapture + injection
//     instead — no global mutation, so it is safe under parallel + async writes.
//
// Coverage boundary (per ai-robust.md "工具选定后强制盲区自检"): this rule bans
// slog.SetDefault — the handler swap that causes the #1490 class. It does NOT
// cover slog.SetLogLoggerLevel (a level-only mutation of the default; no repo
// usages; it does not swap the handler, so it cannot route an async record to a
// foreign sink). That narrower primitive is intentionally out of scope.
//
// Funnel closure (per ai-robust.md "只锁 callsite 不是闭环 funnel"):
//   - Direct call (slog.SetDefault(...), alias / dot-import proof via go/types)
//     → TestSlogCaptureGlobalFunnel.
//   - Indirect reference (var f = slog.SetDefault, pointer pass-through,
//     struct-field binding) → TestSlogCaptureGlobalFunnel_NoIndirectReferences.
//   Together they make the primitive unreachable from disallowed packages by any
//   syntactic shape.
//
// AI-robust grading: Medium. The banned-symbol identity is resolved by go/types
// (IsCallToPkgFunc / info.Uses, alias- and dot-import-proof) — Hard for "did code
// reach this symbol". But the axis "WHICH packages may call a given function" is
// not expressible in the Go type system; no type-level gate can forbid an
// arbitrary package from calling stdlib slog.SetDefault. That permanent
// Go-language ceiling is the same shape as #1352 / #851 / #893 (archtest-bound,
// not compile-bound).
//
// Blind spots of the chosen tool (per ai-robust.md "工具选定后强制盲区自检"):
//   - The sanctioned holder pkg/testutil/slogcapture itself calls slog.SetDefault
//     inside InstallDefault — that is the single allowlisted site, closed by the
//     allowlist (not a leak).
//   - Reflect call: reflect.ValueOf(slog.SetDefault).Call(...). The reflect
//     ValueOf argument is an indirect reference caught by the reverse self-test;
//     the .Call dispatch itself is not modeled (accepted: a test reflecting over
//     SetDefault to evade this rule is implausible and review-visible).
//   - Stdlib drift: slog.SetDefault is a stdlib symbol (cannot be renamed by this
//     repo). If a future Go release removed/renamed it the detector would silently
//     no-op — closed by TestSlogCaptureGlobalFunnel_StdlibSymbolSentinel.
//
// See pkg/testutil/slogcapture/slogcapture.go godoc, runtime/http/health/
// healthtest/slogcapture.go (NewCapture delegates here), and
// tools/archtest/test_eventually_funnel_test.go (the sibling test-helper funnel
// this rule is modeled on).

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

// slogCaptureAllowlist is the closed, shrink-only set of rel-path dir prefixes
// whose code may call the global-mutating primitive slog.SetDefault. Exactly one
// entry: the sanctioned holder. See the file godoc for why.
var slogCaptureAllowlist = []string{
	"pkg/testutil/slogcapture/",
}

const slogCaptureFunnelReason = "raw slog.SetDefault mutates the process-global slog.Default() " +
	"(races parallel siblings under async log writes, #1490); only pkg/testutil/slogcapture may call it — " +
	"redirect the default via slogcapture.InstallDefault (or healthtest.NewCapture, which delegates to it); " +
	"components that accept an injected logger use healthtest.NewLoggerCapture + injection"

const slogCaptureIndirectReason = "indirect reference to slog.SetDefault (function value / pointer " +
	"pass-through / struct-field binding); it mutates global slog.Default() (#1490) — redirect via " +
	"slogcapture.InstallDefault at the callsite instead"

type slogCaptureFunnelViolation struct {
	File   string
	Line   int
	Reason string
}

// isSlogCaptureAllowlistedCaller reports whether rel lives under the sanctioned
// holder directory (allowlist membership is by file path, robust to test-package
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

// isRawSlogSetDefaultFunc reports whether obj is the stdlib log/slog.SetDefault
// function. Single membership check shared by the direct-call and indirect-ref
// scanners (keeps "what counts as banned" single-sourced). Reuses the slogFunnel*
// consts declared by slog_handler_sealed_funnel_test.go (same package).
func isRawSlogSetDefaultFunc(obj types.Object) bool {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() == nil {
		return false
	}
	return fn.Pkg().Path() == slogFunnelStdlibPkgPath && fn.Name() == slogFunnelSetDefaultFunc
}

// directSetDefaultCalleeIdent returns the ident naming the callee of a
// slog.SetDefault call: sel.Sel for the qualified form (slog.SetDefault) or the
// bare ident for the dot-import form (SetDefault). Used to exclude direct calls
// from the indirect-reference reverse self-test (so they are not double-reported).
func directSetDefaultCalleeIdent(call *ast.CallExpr) *ast.Ident {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		return fun.Sel
	case *ast.Ident:
		return fun
	}
	return nil
}

// scanFileForSlogCaptureFunnelViolations returns direct-call violations of
// SLOG-CAPTURE-GLOBAL-FUNNEL-01 in one file. Allowlisted-holder files yield none.
// Resolution is via go/types (IsCallToPkgFunc, alias- and dot-import-proof).
// Shared by the module-wide test and the fixture test.
func scanFileForSlogCaptureFunnelViolations(
	fset *token.FileSet, file *ast.File, info *types.Info, rel string,
) []slogCaptureFunnelViolation {
	if isSlogCaptureAllowlistedCaller(rel) {
		return nil
	}
	var violations []slogCaptureFunnelViolation
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !IsCallToPkgFunc(info, call, slogFunnelStdlibPkgPath, slogFunnelSetDefaultFunc) {
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
// over all sources (Tests:true — slog.SetDefault is a test-scope concern here,
// production seals are governed by SLOG-HANDLER-SEALED-FUNNEL-01). Two loads
// (default + flat non-default tags) union build-tagged files. The current tree
// is GREEN: every test that redirects slog.Default() routes through
// slogcapture.InstallDefault; the only remaining raw slog.SetDefault is inside the
// allowlisted holder pkg/testutil/slogcapture.
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
		"%s: raw slog.SetDefault may only be called from pkg/testutil/slogcapture; tests must "+
			"redirect slog.Default() via slogcapture.InstallDefault (or healthtest.NewCapture, which "+
			"delegates to it), and component tests with an injected logger use healthtest.NewLoggerCapture "+
			"(avoids the async/parallel race #1490). "+
			"Run: go test -tags=archtest -count=1 ./tools/archtest/... -run TestSlogCaptureGlobalFunnel$ for local repro.",
		ruleSlogCaptureGlobalFunnel01)
}

// TestSlogCaptureGlobalFunnelFixtures verifies the direct-call rule against
// static fixtures. positive_green redirects via slogcapture.InstallDefault (the
// sanctioned path) → empty golden; disallowed_caller_red calls raw slog.SetDefault
// from a non-allowlisted package → one violation.
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

// collectDirectSetDefaultCallIdents returns the idents naming the callee of a
// direct slog.SetDefault call. The reverse self-test excludes these so direct
// calls (caught by the main rule) aren't double-reported.
func collectDirectSetDefaultCallIdents(file *ast.File, info *types.Info) map[*ast.Ident]struct{} {
	out := make(map[*ast.Ident]struct{})
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !IsCallToPkgFunc(info, call, slogFunnelStdlibPkgPath, slogFunnelSetDefaultFunc) {
			return
		}
		if id := directSetDefaultCalleeIdent(call); id != nil {
			out[id] = struct{}{}
		}
	})
	return out
}

// scanFileForIndirectSetDefaultReferences returns references to slog.SetDefault
// that are NOT direct calls (function value, pointer pass-through, struct-field
// binding) — the indirect-shape bypass of the CallExpr-driven main rule.
// Allowlisted-holder files yield none.
func scanFileForIndirectSetDefaultReferences(
	p *Pass, file *ast.File, rel string,
) []slogCaptureFunnelViolation {
	if isSlogCaptureAllowlistedCaller(rel) {
		return nil
	}
	directCall := collectDirectSetDefaultCallIdents(file, p.TypesInfo)
	absFile := p.Abs(file)
	seenIdent := make(map[*ast.Ident]struct{})
	var out []slogCaptureFunnelViolation

	for ident, obj := range p.TypesInfo.Uses {
		if ident == nil || obj == nil {
			continue
		}
		if !isRawSlogSetDefaultFunc(obj) {
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
// self-test (ai-robust.md "工具选定后强制盲区自检"): slog.SetDefault must not be
// reachable from disallowed packages via indirect references either. Together
// with the main rule this closes the funnel ("只锁 callsite 不是闭环 funnel").
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
			for _, v := range scanFileForIndirectSetDefaultReferences(p, file, rel) {
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
		"%s blind-spot reverse self-test: slog.SetDefault must not be reachable via indirect "+
			"references (function value, pointer pass-through, struct field) from disallowed packages; "+
			"redirect via slogcapture.InstallDefault. "+
			"Run: go test -tags=archtest -count=1 ./tools/archtest/... -run TestSlogCaptureGlobalFunnel_NoIndirectReferences$ for local repro.",
		ruleSlogCaptureGlobalFunnel01)
}

// TestSlogCaptureGlobalFunnel_NoIndirectReferences_Fixtures proves the indirect
// scanner is wired (anti-vacuity): indirect_ref_red binds slog.SetDefault to a
// function-value var, struct_field_ref_red binds it to a struct field → one
// violation each. Without these RED fixtures the module-wide reverse test is
// always green (current tree has no indirect refs).
func TestSlogCaptureGlobalFunnel_NoIndirectReferences_Fixtures(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	dirs := []string{"indirect_ref_red", "struct_field_ref_red"}
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
					for _, v := range scanFileForIndirectSetDefaultReferences(p, file, rel) {
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

// TestSlogCaptureGlobalFunnel_StdlibSymbolSentinel locks the banned-symbol
// identity against stdlib drift. The banned surface is stdlib log/slog.SetDefault
// (this repo cannot rename it); but if a future Go release renamed/removed it, the
// IsCallToPkgFunc / info.Uses predicates would silently stop firing and the funnel
// would become a no-op. This sentinel walks loaded package scopes and fails if
// log/slog.SetDefault is no longer a function — pointing the reviewer here to
// update slogFunnelSetDefaultFunc / the detector if the stdlib shape changed.
//
// (Threat-model note per ai-robust.md: the prior wrapper-based rule's sentinel
// guarded a PROJECT-symbol rename; sealing the stdlib primitive shifts the
// residual drift axis to stdlib API change.)
func TestSlogCaptureGlobalFunnel_StdlibSymbolSentinel(t *testing.T) {
	t.Parallel()

	found := false
	scan := func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		for _, imp := range p.Pkg.Imports() {
			if imp.Path() != slogFunnelStdlibPkgPath {
				continue
			}
			if obj := imp.Scope().Lookup(slogFunnelSetDefaultFunc); obj != nil {
				if _, ok := obj.(*types.Func); ok {
					found = true
				}
			}
		}
		return nil
	}

	_ = Run(t, Typed(TypedOpts{Tests: true}, []string{"./..."}), scan)

	assert.True(t, found,
		"%s sentinel: stdlib %s.%s not found as a function in any loaded scope — renamed/removed "+
			"by the toolchain? This funnel silently stops guarding the global-slog primitive. Update "+
			"slogFunnelSetDefaultFunc AND verify the IsCallToPkgFunc predicate covers the new shape.",
		ruleSlogCaptureGlobalFunnel01, slogFunnelStdlibPkgPath, slogFunnelSetDefaultFunc)
}

func sortSlogCaptureViolations(v []slogCaptureFunnelViolation) {
	sort.Slice(v, func(i, j int) bool {
		if v[i].File != v[j].File {
			return v[i].File < v[j].File
		}
		return v[i].Line < v[j].Line
	})
}
