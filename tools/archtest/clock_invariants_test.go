// invariants:
//   - INVARIANT: CLOCK-INJECTION-TEST-CALLSITE-01
//   - INVARIANT: CLOCK-INJECTION-PROD-CALLSITE-01
//   - INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//   - INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//   - INVARIANT: PROD-CLOCK-INJECTION-01
//
// Package archtest — clock injection invariants.
//
// Merged from:
//   - clock_injection_callsite_test.go      (CLOCK-INJECTION-TEST-CALLSITE-01)
//   - clock_injection_prod_callsite_test.go (CLOCK-INJECTION-PROD-CALLSITE-01)
//   - clock_leaf_fallback_test.go           (KERNEL-CLOCK-LEAF-FALLBACK-01)
//   - clock_reset_relative_prod_test.go     (KERNEL-CLOCK-RESET-RELATIVE-PROD-01)
//   - prod_clock_injection_test.go          (PROD-CLOCK-INJECTION-01)
//
// Note: prod_clock_injection_fixtures_test.go is a companion fixture file and
// is kept separate (not merged).
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ---------------------------------------------------------------------------
// CLOCK-INJECTION-TEST-CALLSITE-01
// ---------------------------------------------------------------------------

// clockViaSliceAllowMarker is the annotation that exempts a constructor call
// from CLOCK-INJECTION-TEST-CALLSITE-01 when Go syntax prevents passing
// WithClock as a direct arg (e.g. options come from a dynamically-built slice
// that already includes WithClock, and positional + spread is not valid Go).
// The annotation must appear on the same line as the call's closing ")" with
// a non-empty reason: `//archtest:allow:clock-injection:via-slice <reason>`.
const clockViaSliceAllowMarker = "//archtest:allow:clock-injection:via-slice"

// clockCallsiteAllowedLines returns the set of source line numbers in file
// that carry a valid clockViaSliceAllowMarker with a non-empty reason.
func clockCallsiteAllowedLines(fset *token.FileSet, file *ast.File) map[int]bool {
	out := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			text := strings.TrimSpace(c.Text)
			if !strings.HasPrefix(text, clockViaSliceAllowMarker) {
				continue
			}
			// Require a non-empty reason after the marker.
			rest := strings.TrimSpace(strings.TrimPrefix(text, clockViaSliceAllowMarker))
			if rest == "" {
				continue
			}
			line := fset.Position(c.Slash).Line
			out[line] = true
		}
	}
	return out
}

// clockRequiredCtor holds a collected constructor whose package has a WithClock
// option function.
type clockRequiredCtor struct {
	ctorFullName      string // key: ctor.FullName()
	withClockFullName string // key: withClock.FullName()
}

// collectClockRequiredCtorsFromPass scans a single package's types for
// constructors (func name starting with "New", last param variadic) whose
// package also exports a "WithClock" function. Returns entries to add to the
// global ctors map.
//
// This is the per-Pass equivalent of the old collectClockRequiredCtors that
// operated on []*packages.Package. RunTyped calls this once per package; the
// caller accumulates results into a shared map keyed by FullName() strings.
//
// Using FullName() strings instead of *types.Func pointers avoids false
// mismatches between the test-variant and non-test-variant of the same package
// (RunTyped's dedup-by-*ast.File ensures files are not double-counted, but the
// same *types.Package may appear via two load variants; keying on path-stable
// FullName() strings provides the same guarantee as the original).
func collectClockRequiredCtorsFromPass(p *Pass) []clockRequiredCtor {
	if p.Pkg == nil {
		return nil
	}
	scope := p.Pkg.Scope()

	// Check if this package exports WithClock.
	obj := scope.Lookup("WithClock")
	if obj == nil {
		return nil
	}
	fn, ok := obj.(*types.Func)
	if !ok {
		return nil
	}
	withClockFullName := fn.FullName()

	// Collect New* constructors from this package (variadic last param).
	var result []clockRequiredCtor
	for _, name := range scope.Names() {
		if !strings.HasPrefix(name, "New") {
			continue
		}
		cobj := scope.Lookup(name)
		cfn, ok := cobj.(*types.Func)
		if !ok {
			continue
		}
		sig, ok := cfn.Type().(*types.Signature)
		if !ok || !sig.Variadic() {
			continue
		}
		result = append(result, clockRequiredCtor{
			ctorFullName:      cfn.FullName(),
			withClockFullName: withClockFullName,
		})
	}
	return result
}

// callsWithClock reports whether any of the call arguments in parent contains a
// direct CallExpr whose callee's FullName matches withClockFullName.
func callsWithClock(parent *ast.CallExpr, info *types.Info, withClockFullName string) bool {
	found := false
	for _, arg := range parent.Args {
		EachInSubtree[ast.CallExpr](arg, func(call *ast.CallExpr) {
			if found {
				return
			}
			fn := resolvedFunc(call.Fun, info)
			if fn != nil && fn.FullName() == withClockFullName {
				found = true
			}
		})
	}
	return found
}

// resolvedFunc returns the *types.Func for a call expression's function
// expression, or nil if it cannot be determined.
func resolvedFunc(fun ast.Expr, info *types.Info) *types.Func {
	if info == nil {
		return nil
	}
	var ident *ast.Ident
	switch e := fun.(type) {
	case *ast.Ident:
		ident = e
	case *ast.SelectorExpr:
		ident = e.Sel
	default:
		return nil
	}
	obj, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return nil
	}
	return obj
}

// scanClockCallsiteAST walks file looking for calls to any constructor in
// ctors from test files, and reports violations where WithClock is missing.
//
// A call may be exempted by placing `//archtest:allow:clock-injection:via-slice
// <reason>` on the same line as the call's closing ")" when Go syntax prevents
// passing WithClock as a direct positional arg (e.g. options live in a
// dynamically-built slice that already contains WithClock).
func scanClockCallsiteAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	ctors map[string]clockRequiredCtor,
) []Diagnostic {
	allowedLines := clockCallsiteAllowedLines(fset, file)
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		callee := resolvedFunc(call.Fun, info)
		if callee == nil {
			return
		}
		ctor, isCtor := ctors[callee.FullName()]
		if !isCtor {
			return
		}
		// Only flag calls that have at least one option argument (variadic slice
		// non-empty). A bare NewXxx() with zero options has no WithXxx at all,
		// which may be intentional (e.g. constructor with zero required options).
		// We only flag the case where options ARE passed but WithClock is absent.
		if len(call.Args) == 0 {
			return
		}
		if callsWithClock(call, info, ctor.withClockFullName) {
			return
		}
		// Check for explicit exemption via allow-marker on the closing-paren line.
		closingLine := fset.Position(call.Rparen).Line
		if allowedLines[closingLine] {
			return
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s", rel, line, callee.Name())
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"%s called without WithClock — "+
						"must pass WithClock(clk) to satisfy the clock injection requirement. "+
						"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md",
					callee.FullName()),
			})
		}
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// INVARIANT: CLOCK-INJECTION-TEST-CALLSITE-01
//
// TestClockInjectionCallsite enforces CLOCK-INJECTION-TEST-CALLSITE-01:
// test files must pass WithClock when calling constructors whose package
// exports WithClock.
//
// Detection strategy (option-pattern only, v1):
//  1. Load all packages with tests=true and the integration+e2e build tags.
//  2. For each non-test file, collect packages that export a function named
//     "WithClock" (the canonical Clock option injector). Record the package
//     path and the set of constructor names — functions in the same package
//     whose last parameter is variadic and whose name starts with "New".
//  3. Scan each test file for CallExpr whose callee resolves (via go/types) to
//     one of the collected constructors.
//  4. For each such call, walk the argument list looking for a nested CallExpr
//     whose callee resolves to the WithClock function of the same package.
//  5. If no WithClock call is found among the arguments — report a violation.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
func TestClockInjectionCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	// Phase 1: collect all ctors from packages that have WithClock.
	// Load with tests=true so we see every package variant.
	ctors := make(map[string]clockRequiredCtor)
	_ = RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, patterns,
		func(p *Pass) []Diagnostic {
			for _, c := range collectClockRequiredCtorsFromPass(p) {
				ctors[c.ctorFullName] = c
			}
			return nil
		})

	// Phase 2: scan test files for callsite violations.
	diags := RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, patterns,
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				// Only scan test files.
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				d = append(d, scanClockCallsiteAST(p.Fset, f, rel, p.TypesInfo, ctors)...)
			}
			return d
		})

	Report(t, "CLOCK-INJECTION-TEST-CALLSITE-01", diags)
}

// runClockCallsiteFixtureScan loads a fixture directory and returns violations.
func runClockCallsiteFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()

	// Phase 1: collect ctors from the fixture module.
	ctors := make(map[string]clockRequiredCtor)
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: true}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			for _, c := range collectClockRequiredCtorsFromPass(p) {
				ctors[c.ctorFullName] = c
			}
			return nil
		})

	// Phase 2: scan test files for callsite violations.
	return RunTypedDir(t, fixtureDir, TypedOpts{Tests: true}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				d = append(d, scanClockCallsiteAST(p.Fset, f, rel, p.TypesInfo, ctors)...)
			}
			return d
		})
}

// TestClockInjectionCallsiteFixtures validates fixture-based regression cases.
func TestClockInjectionCallsiteFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/clock_injection_callsite_fixtures"

	cases := []struct {
		pkg           string
		wantViolCount int
	}{
		{"compliant", 0},
		{"violates", 1},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runClockCallsiteFixtureScan(t, base+"/"+tc.pkg)
			assert.Equal(t, tc.wantViolCount, len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.pkg, tc.wantViolCount, len(got), got)
		})
	}
}

// ---------------------------------------------------------------------------
// CLOCK-INJECTION-PROD-CALLSITE-01
// ---------------------------------------------------------------------------

// isCompositionRoot reports whether the given module-relative path is a
// production composition-root file for CLOCK-INJECTION-PROD-CALLSITE-01.
//
// Composition roots are:
//   - any non-test .go file under cmd/
//   - any main.go file under examples/ at any depth
//
// Intentionally NOT flagging cells/, runtime/, kernel/ — those are injection
// targets, not composition roots.
func isCompositionRoot(rel string) bool {
	if rel == "" {
		return false
	}
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	if strings.HasPrefix(rel, "tools/archtest/") {
		return false
	}
	if strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/") {
		return false
	}
	// cmd/: all non-test Go files
	if strings.HasPrefix(rel, "cmd/") {
		return true
	}
	// examples/: only main.go files (composition roots, not library code)
	if strings.HasPrefix(rel, "examples/") && strings.HasSuffix(rel, "/main.go") {
		return true
	}
	return false
}

// compositionRootDirExists checks whether a directory exists under root.
func compositionRootDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// INVARIANT: CLOCK-INJECTION-PROD-CALLSITE-01
//
// TestClockInjectionProdCallsite enforces CLOCK-INJECTION-PROD-CALLSITE-01:
// production composition-root files (cmd/ + examples/*/main.go) must pass
// WithClock when calling constructors whose package exports WithClock.
//
// This is the production-side complement to CLOCK-INJECTION-TEST-CALLSITE-01
// which only scans *_test.go files. Together they enforce clock injection
// at every composition boundary.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
// ref: uber-go/fx fx.Provide DI graph validation
func TestClockInjectionProdCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// Build patterns covering only cmd/ and examples/ (the composition roots).
	// We deliberately exclude cells/runtime/kernel/ — those are injection targets.
	var patterns []string
	for _, dir := range []string{"cmd", "examples"} {
		if compositionRootDirExists(filepath.Join(root, dir)) {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	if len(patterns) == 0 {
		t.Skip("no cmd/ or examples/ directories found")
	}

	// Load without tests=true — composition root files are not test files.

	// Phase 1: collect all ctors from packages that have WithClock.
	ctors := make(map[string]clockRequiredCtor)
	_ = RunTyped(t, TypedOpts{Tests: false}, patterns,
		func(p *Pass) []Diagnostic {
			for _, c := range collectClockRequiredCtorsFromPass(p) {
				ctors[c.ctorFullName] = c
			}
			return nil
		})

	// Phase 2: scan composition-root files for callsite violations.
	diags := RunTyped(t, TypedOpts{Tests: false}, patterns,
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !isCompositionRoot(rel) {
					continue
				}
				d = append(d, scanClockCallsiteAST(p.Fset, f, rel, p.TypesInfo, ctors)...)
			}
			return d
		})

	Report(t, "CLOCK-INJECTION-PROD-CALLSITE-01", diags)
}

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-LEAF-FALLBACK-01
// ---------------------------------------------------------------------------

// kernelClockPkgPath is the import path of the package whose Real() factory
// the gate guards. Hard-coded so a rename of the clock package is loud
// (test breaks and forces explicit migration).
const kernelClockPkgPath = "github.com/ghbvf/gocell/kernel/clock"

// allowedRealCallerPaths lists the production-code paths that may call
// kernel/clock.Real() directly. See package doc for rationale.
//
// Each entry is matched as either an exact file path (when it ends in .go)
// or a directory prefix (otherwise). The exact-file form lets us exempt
// kernel/clock/clock.go (the Real() factory definition itself) without
// exempting kernel/clock/clockmock or any future sibling packages.
var allowedRealCallerPaths = []string{
	"kernel/clock/clock.go",                 // Real() factory definition
	"cmd/corebundle/",                       // main composition root
	"cmd/gocell/",                           // gocell CLI composition root
	"gocell.go",                             // top-level entry
	"tests/e2e/internal/clients/clients.go", // e2e suite composition root
	// examples/ is excluded by fileroles.IsProductionCode (see package doc),
	// so example composition roots (examples/iotdevice/main.go,
	// examples/ssobff/app.go, examples/todoorder/main.go) do not need
	// allowlist entries.
	//
	// Test-helper packages own clock.Real() construction so test callers
	// don't repeat it. They are imported only by *_test.go files; the
	// CLOCK-INJECTION-TEST-CALLSITE-01 archtest enforces that boundary.
	"cells/accesscore/internal/testutil/", // SessionRepoForTest / RealSessionRepo
}

// INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//
// TestKernelClockLeafFallback enforces KERNEL-CLOCK-LEAF-FALLBACK-01:
// leaf-level clock.Real() construction is forbidden outside the composition root.
//
// Invariant: In every Go file whose role is "production code"
// (tools/internal/fileroles.IsProductionCode), there must be no direct call
// to kernel/clock.Real(). The single root Clock is constructed once at the
// composition root and threaded through every consumer; any leaf-level
// fallback re-introduces the wall-clock surface that PROD-CLOCK-INJECTION-01
// was meant to abstract over.
//
// Resolution is type-driven: every *ast.SelectorExpr is run through
// go/types.Info.ObjectOf to obtain the resolved *types.Func, then gated on
// obj.Pkg().Path() == "github.com/ghbvf/gocell/kernel/clock" and
// obj.Name() == "Real". This makes the check immune to import aliases and
// dot-imports.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6 closure
func TestKernelClockLeafFallback(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	diags := RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, patterns,
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if isAllowedRealCallerPath(rel) {
					continue
				}
				d = append(d, scanLeafRealCallsAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})

	Report(t, "KERNEL-CLOCK-LEAF-FALLBACK-01", diags)
}

// isAllowedRealCallerPath reports whether rel is exempt from the gate.
// Entries ending in .go match exactly; other entries match as directory
// prefixes.
func isAllowedRealCallerPath(rel string) bool {
	for _, allowed := range allowedRealCallerPaths {
		if strings.HasSuffix(allowed, ".go") {
			if rel == allowed {
				return true
			}
			continue
		}
		if strings.HasPrefix(rel, allowed) {
			return true
		}
	}
	return false
}

// scanLeafRealCallsAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every call to kernel/clock.Real(). Detection
// is type-driven via info.ObjectOf so import aliases and dot-imports are
// uniformly covered.
func scanLeafRealCallsAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	record := func(node ast.Node) {
		line := fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: "kernel/clock.Real() — accept clock.Clock as a constructor " +
				"parameter and validate via clock.MustHaveClock",
		})
	}

	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		// Standard form: clock.Real / c.Real (alias).
		if matchedKernelClockReal(info, e.Sel) {
			record(e)
		}
	})
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		// Dot-import form: `import . "…/kernel/clock"; Real()`.
		if matchedKernelClockReal(info, e) {
			record(e)
		}
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// matchedKernelClockReal reports whether ident resolves (via info.ObjectOf)
// to the package-level function kernel/clock.Real.
//
// Filters explicitly to *types.Func with a nil receiver so that references
// to a Real type / Real const / Real method on an unrelated package are
// not flagged.
func matchedKernelClockReal(info *types.Info, ident *ast.Ident) bool {
	if info == nil || ident == nil {
		return false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != kernelClockPkgPath {
		return false
	}
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	return fn.Name() == "Real"
}

// runLeafFallbackFixtureScan loads the fixture package at fixtureDir and
// returns the sorted slice of violation Diagnostics using the same predicate
// as TestKernelClockLeafFallback (scanLeafRealCallsAST). The whitelist is
// intentionally NOT applied here — every fixture path is treated as
// production code so the gate's detection logic is the only thing under test.
func runLeafFallbackFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, scanLeafRealCallsAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// TestKernelClockLeafFallbackFixtures runs the KERNEL-CLOCK-LEAF-FALLBACK-01
// scanner over each fixture subpackage and asserts the expected violation
// count. Mirrors TestProdClockInjectionFixtures (sibling gate).
func TestKernelClockLeafFallbackFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/clock_leaf_fallback_fixtures"

	cases := []struct {
		pkg          string
		wantViolReps int // expected number of (file:line) violation reports
	}{
		// Positive — must produce 0 violations.
		{"compliant", 0},
		// Negative — exercises three call shapes (direct, alias, nil-fallback).
		// Each produces one (file:line) report (selector + alias + body all on
		// distinct lines), so 3 reports total.
		{"violates", 3},
	}

	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			t.Parallel()
			got := runLeafFallbackFixtureScan(t, base+"/"+tc.pkg)
			assert.Equal(t, tc.wantViolReps, len(got),
				"fixture %s: expected %d violation report(s), got %d: %v",
				tc.pkg, tc.wantViolReps, len(got), got)
		})
	}
}

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-RESET-RELATIVE-PROD-01
// ---------------------------------------------------------------------------

// clockResetRelativeExemptPaths lists path prefixes exempt from the gate.
// These packages define or implement the Reset method itself, not callers.
var clockResetRelativeExemptPaths = []string{
	"kernel/clock/clock.go",   // interface definition
	"kernel/clock/clockmock/", // fake implementation
}

// INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//
// TestKernelClockResetRelativeProd enforces KERNEL-CLOCK-RESET-RELATIVE-PROD-01:
// production code must use the absolute Timer.ResetAt(deadline time.Time) API
// instead of the relative Timer.Reset(d time.Duration) to eliminate the
// read-then-act race between capturing a deadline and arming the timer.
//
// Detection is type-driven (go/types): identifies any CallExpr `<expr>.Reset(<arg>)`
// where the resolved *types.Func has Reset(time.Duration) bool signature and the
// receiver also exposes ResetAt — the structural marker for clock.Timer-like types.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
func TestKernelClockResetRelativeProd(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	diags := RunTyped(t, TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}, patterns,
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if isClockResetRelativeExempt(rel) {
					continue
				}
				d = append(d, scanClockResetRelativeAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})

	Report(t, "KERNEL-CLOCK-RESET-RELATIVE-PROD-01", diags)
}

// isClockResetRelativeExempt reports whether rel is exempt from the gate.
func isClockResetRelativeExempt(rel string) bool {
	for _, prefix := range clockResetRelativeExemptPaths {
		if strings.HasPrefix(rel, prefix) || rel == prefix {
			return true
		}
	}
	return false
}

// scanClockResetRelativeAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every call `<expr>.Reset(d)` where the receiver
// structurally implements kernel/clock.Timer (has both Reset(Duration)bool and
// ResetAt(Time)bool methods). This is the same predicate used by the fixture
// regression tests.
func scanClockResetRelativeAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Reset" {
			return
		}
		if info == nil {
			return
		}
		fn, ok := info.ObjectOf(sel.Sel).(*types.Func)
		if !ok || fn == nil {
			return
		}
		sig, ok := fn.Type().(*types.Signature)
		if !ok {
			return
		}
		// Must be a method (has receiver).
		if sig.Recv() == nil {
			return
		}
		// Signature: Reset(time.Duration) bool
		if !isResetDurationBool(sig) {
			return
		}
		// Receiver type must also expose ResetAt(time.Time) bool — the
		// structural marker for clock.Timer-like types.
		recvType := sig.Recv().Type()
		if !typeHasResetAt(recvType) {
			return
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "Timer.Reset(d time.Duration) — use ResetAt(deadline time.Time) instead " +
					"to avoid read-then-act race; " +
					"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md",
			})
		}
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// isResetDurationBool reports whether sig matches Reset(time.Duration) bool.
func isResetDurationBool(sig *types.Signature) bool {
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	param := sig.Params().At(0).Type()
	result := sig.Results().At(0).Type()
	if !isTimeDurationType(param) {
		return false
	}
	basic, ok := result.(*types.Basic)
	return ok && basic.Kind() == types.Bool
}

// typeHasResetAt reports whether t (or its underlying pointer/interface) exposes
// a method named "ResetAt" with signature ResetAt(time.Time) bool.
func typeHasResetAt(t types.Type) bool {
	mset := types.NewMethodSet(t)
	sel := mset.Lookup(nil, "ResetAt")
	if sel == nil {
		// Try pointer receiver too.
		mset = types.NewMethodSet(types.NewPointer(t))
		sel = mset.Lookup(nil, "ResetAt")
	}
	if sel == nil {
		return false
	}
	fn, ok := sel.Obj().(*types.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	if sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return false
	}
	param := sig.Params().At(0).Type()
	result := sig.Results().At(0).Type()
	basic, ok := result.(*types.Basic)
	if !ok || basic.Kind() != types.Bool {
		return false
	}
	return isTimeTimeType(param)
}

// isTimeDurationType reports whether t is time.Duration.
func isTimeDurationType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "time" && obj.Name() == "Duration"
}

// isTimeTimeType reports whether t is time.Time.
func isTimeTimeType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "time" && obj.Name() == "Time"
}

// TestKernelClockResetRelativeFixtures verifies the scanner against the two
// fixture packages: one that violates the rule, one that is compliant.
func TestKernelClockResetRelativeFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := root + "/tools/archtest/testdata/clock_reset_relative_fixtures"

	cases := []struct {
		dir           string
		wantViolLines []int // nil = expect 0 violations
	}{
		{"compliant", nil},
		{"violates", []int{17}},
	}

	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := fixturesBase + "/" + tc.dir
			got := runClockResetRelativeFixtureScan(t, fixtureDir)

			if len(tc.wantViolLines) == 0 {
				assert.Empty(t, got, "fixture %s: expected 0 violations, got %v", tc.dir, got)
				return
			}

			assert.Equal(t, len(tc.wantViolLines), len(got),
				"fixture %s: expected %d violation(s), got %d: %v",
				tc.dir, len(tc.wantViolLines), len(got), got)

			for i, wantLine := range tc.wantViolLines {
				if i >= len(got) {
					break
				}
				assert.Equal(t, "usage.go", got[i].Rel,
					"fixture %s violation[%d]: expected Rel=usage.go, got %q",
					tc.dir, i, got[i].Rel)
				assert.Equal(t, wantLine, got[i].Line,
					"fixture %s violation[%d]: expected Line=%d, got %d",
					tc.dir, i, wantLine, got[i].Line)
			}
		})
	}
}

// runClockResetRelativeFixtureScan loads a standalone fixture module and runs
// the same scanner as TestKernelClockResetRelativeProd.
func runClockResetRelativeFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, scanClockResetRelativeAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// ---------------------------------------------------------------------------
// PROD-CLOCK-INJECTION-01
// ---------------------------------------------------------------------------

// allowedRealClockPaths lists the prefixes whose files may legitimately
// reference stdlib time symbols directly. See package doc for rationale.
var allowedRealClockPaths = []string{
	"kernel/clock/",
}

// forbiddenTimeFns maps each forbidden stdlib time function to the equivalent
// Clock interface method that production callers must use instead.
//
// Note that time.After is mapped to NewTimerAt because the channel-returning
// shortcut form has no ctx-aware analog; callers must rewrite as
// `timer := clk.NewTimerAt(deadline); defer timer.Stop()` +
// `select { case <-ctx.Done(): ... case <-timer.C(): ... }`.
//
// time.NewTicker and time.Tick map to clock.Clock.NewTicker (interval-based,
// matching stdlib semantics: first fire at Now()+interval). The Clock
// interface deliberately does not expose a "TickerAt" form — the
// duration-based shape carries the same read-then-act gap on the very first
// tick that NewTimerAt was designed to eliminate, but for tickers the
// stdlib parity is more valuable than the absolute-deadline guarantee.
var forbiddenTimeFns = map[string]string{
	"Now":       "clock.Clock.Now",
	"Since":     "clock.Clock.Since",
	"Until":     "clock.Clock.Until",
	"NewTimer":  "clock.Clock.NewTimerAt",
	"NewTicker": "clock.Clock.NewTicker",
	"After":     "clock.Clock.NewTimerAt",
	"AfterFunc": "clock.Clock.AfterFunc",
	"Tick":      "clock.Clock.NewTicker",
	"Sleep":     "clock.Clock.Sleep",
}

// INVARIANT: PROD-CLOCK-INJECTION-01
//
// TestProdClockInjection enforces PROD-CLOCK-INJECTION-01 against the
// production tree: no direct reference to stdlib time wall-clock entry points
// (time.Now, time.Since, time.Until, time.NewTimer, etc.) in production code.
// All wall-clock interactions must flow through an injected kernel/clock.Clock.
//
// Resolution is type-driven: every *ast.SelectorExpr and bare *ast.Ident is
// run through go/types.Info.ObjectOf to obtain the resolved *types.Func, then
// gated on obj.Pkg().Path() == "time" and obj.Name() in forbiddenTimeFns.
// This makes the check immune to import aliases and dot-imports.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
// ref: dominikh/go-tools analysis/code/code.go CallName / IsCallToAny
func TestProdClockInjection(t *testing.T) {
	t.Parallel()
	// Intentionally not honoring testing.Short — see TestTestTimeLiteralConst.

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	diags := RunTyped(t, TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}, patterns,
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if isAllowedRealClockPath(rel) {
					continue
				}
				d = append(d, scanProdClockInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})

	Report(t, "PROD-CLOCK-INJECTION-01", diags)
}

// isAllowedRealClockPath reports whether rel falls under one of the
// allowedRealClockPaths roots.
func isAllowedRealClockPath(rel string) bool {
	for _, prefix := range allowedRealClockPaths {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// scanProdClockInjectionAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every reference to one of the forbidden stdlib
// time functions. Both call positions (time.Now()) and function-value
// positions (now := time.Now, struct field assignment `{now: time.Now}`) are
// detected. Detection is type-driven via info.ObjectOf, so import aliases and
// dot-imports are covered uniformly.
//
// scanProdClockInjectionAST is exported within the archtest package so the
// fixture-based regression tests in prod_clock_injection_fixtures_test.go can
// share the exact same predicate.
func scanProdClockInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	record := func(node ast.Node, name string) {
		line := fset.Position(node.Pos()).Line
		key := fmt.Sprintf("%s:%d:%s", rel, line, name)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"time.%s — must use injected %s instead",
				name, forbiddenTimeFns[name]),
		})
	}

	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		// Standard form: time.Now, t.Now (alias), pkg.Now (any pkg).
		// Type info on .Sel resolves the actual function regardless of
		// the receiver identifier name.
		if name, ok := matchedTimeFn(info, e.Sel); ok {
			record(e, name)
		}
	})
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		// Dot-import form: `import . "time"; Now()`. The Ident is the
		// call function reference itself — no SelectorExpr surrounds it.
		if name, ok := matchedTimeFn(info, e); ok {
			record(e, name)
		}
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Message < out[j].Message
	})
	return out
}

// matchedTimeFn reports whether ident resolves (via info.ObjectOf) to a
// package-level function in the standard library's time package whose name is
// in the forbidden set.
//
// Filters explicitly to *types.Func so that legitimate references to
// time.Time (a Type), time.Second (a Const), or any same-named field on a
// non-time package are not flagged.
//
// Methods on time.Time (e.g. t.After(u), t.Before(u)) share the same name as
// the forbidden package-level functions but are semantically unrelated — they
// compare two time.Time values and return bool. They are excluded by checking
// Signature().Recv(): a non-nil receiver indicates a method, not a package-level
// function. Production code should use these method comparisons freely.
func matchedTimeFn(info *types.Info, ident *ast.Ident) (string, bool) {
	if info == nil || ident == nil {
		return "", false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return "", false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != "time" {
		return "", false
	}
	// Exclude methods (e.g. time.Time.After, time.Time.Before) — only flag
	// package-level functions such as time.After(d Duration) <-chan time.Time.
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return "", false
	}
	name := fn.Name()
	if _, listed := forbiddenTimeFns[name]; !listed {
		return "", false
	}
	return name, true
}
