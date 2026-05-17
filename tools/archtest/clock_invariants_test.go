// invariants:
//   - INVARIANT: CLOCK-INJECTION-TEST-CALLSITE-01
//   - INVARIANT: CLOCK-INJECTION-PROD-CALLSITE-01
//   - INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//   - INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//   - INVARIANT: PROD-CLOCK-INJECTION-01
//   - INVARIANT: CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01
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
	"go/parser"
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

// clockControlPlaneAllowMarker is the function-level annotation that exempts
// a *ast.FuncDecl from PROD-CLOCK-INJECTION-01 when the function is a
// named control-plane scheduling primitive that intentionally uses real stdlib
// time (e.g. controlPlaneTicker / controlPlaneProbeTimer in
// runtime/command/lifecycle.go).
//
// The marker must appear in the FuncDecl's doc comment (the CommentGroup
// immediately preceding the "func" keyword) with a non-empty reason:
// `//archtest:allow:clock-injection:control-plane <reason>`
//
// Carve-out scope: function-level only, AND gated by the explicit
// controlPlaneClockCarveOut {rel → func names} allowlist (review P1-3). The
// marker comment alone never exempts: the FuncDecl's (module-relative path,
// name) must also be listed. The enclosing file and package are NOT exempted;
// any other function — including a third function in the same allowlisted
// file — is still checked.
//
// AI-rebust grade: Medium (archtest-enforced: allowlist map + marker, not a
// bare comment a business PR can add anywhere; not compile-time). Before P1-3
// this was effectively Soft (any marked FuncDecl in any prod file self-exempt).
// Hard upgrade path: backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01
// tracks replacing this with a typed funnel (sealed clock-source type) once
// the control-plane clock abstraction is established.
//
// Blind spots (function-level comment guard, AST-based detection):
//   - A FuncDecl without a doc comment group will never match — the marker
//     must appear in the "doc" block, not an inline comment. RED self-check:
//     TestProdClockInjectionControlPlaneMarkerFixtures/no_marker_violates
//     asserts that a function with //archtest:... in a non-doc inline comment
//     is NOT exempted and still produces a violation.
//   - A FuncLit (anonymous function / closure) cannot carry a doc comment;
//     time.* calls inside closures in an exempted FuncDecl body are NOT
//     themselves exempt — only the FuncDecl's direct body statements are
//     within the carve-out scope. RED self-check: closure_violates fixture
//     asserts a closure inside an otherwise-exempt function is flagged.
//   - The marker reason must be non-empty; a bare marker with no trailing
//     text is silently ignored (same rule as clockViaSliceAllowMarker).
const clockControlPlaneAllowMarker = "//archtest:allow:clock-injection:control-plane"

// controlPlaneClockCarveOut is the EXHAUSTIVE {module-relative path → func
// names} allowlist of control-plane clock carve-out sites. The marker comment
// alone is NOT sufficient: a FuncDecl is exempt only if (a) its doc comment
// carries a valid clockControlPlaneAllowMarker AND (b) its (rel, name) pair is
// listed here. This closes review P1-3 — previously any production FuncDecl in
// any file could self-exempt PROD-CLOCK-INJECTION-01 just by adding the marker
// (effectively Soft). The allowlist is the binding truth source; the in-source
// marker is retained for self-documentation + the doc-comment blind-spot
// self-checks.
//
// Adding an entry is a deliberate, reviewable archtest change (not a comment a
// business PR can sneak in). Hard upgrade path (typed real-only clock funnel):
// backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
var controlPlaneClockCarveOut = map[string]map[string]bool{
	"runtime/command/lifecycle.go": {
		"controlPlaneTicker":     true,
		"controlPlaneProbeTimer": true,
	},
}

// clockControlPlaneAllowedFuncs returns the set of FuncDecl name-positions in
// file that are exempt from PROD-CLOCK-INJECTION-01: doc comment carries a
// valid clockControlPlaneAllowMarker (non-empty reason) AND the (rel, name)
// pair is in controlPlaneClockCarveOut. A marker on any other function — a
// third function in the allowlisted file, or any function in any other file /
// package — does NOT exempt (review P1-3 RED self-checks
// control_plane_marker_wrong_func_violates / _wrong_path_violates).
//
// Uses EachInChildren[ast.FuncDecl](file, ...) — top-level FuncDecls are
// direct children of *ast.File, so depth=1 is correct and sufficient.
func clockControlPlaneAllowedFuncs(fset *token.FileSet, file *ast.File, rel string) map[string]bool {
	out := map[string]bool{}
	allowedNames := controlPlaneClockCarveOut[rel]
	if allowedNames == nil {
		return out // rel not an allowlisted carve-out path — nothing is exempt
	}
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Doc == nil || !allowedNames[fd.Name.Name] {
			return
		}
		for _, c := range fd.Doc.List {
			text := strings.TrimSpace(c.Text)
			if !strings.HasPrefix(text, clockControlPlaneAllowMarker) {
				continue
			}
			rest := strings.TrimSpace(strings.TrimPrefix(text, clockControlPlaneAllowMarker))
			if rest == "" {
				continue
			}
			// Use the position of the func name as the unique key.
			key := fset.Position(fd.Name.Pos()).String()
			out[key] = true
		}
	})
	return out
}

// enclosingFuncDeclKey returns the position-string key for the nearest
// enclosing top-level *ast.FuncDecl that directly (not via a FuncLit/closure)
// contains pos. Returns "" if pos is not inside any FuncDecl body, or if pos
// is inside a nested FuncLit within a FuncDecl body.
//
// The key matches the format produced by clockControlPlaneAllowedFuncs.
//
// Why the FuncLit exclusion matters (carve-out boundary):
//
// Without the exclusion, `fd.Body.Pos() <= pos <= fd.Body.End()` is true for
// any code inside the function — including closures/FuncLits. This would
// exempt `time.*` calls inside closures of a marked function, which violates
// the documented carve-out semantics: "the exemption does NOT extend to closures
// within the exempt FuncDecl body."
//
// The exclusion is implemented by walking FuncLits within fd.Body and returning
// "" whenever pos falls inside one.
//
// Blind spots (per ai-collab.md §"工具选定后强制盲区自检"):
//
//  1. FuncLit nested inside another FuncLit inside a marked FuncDecl: also
//     excluded (EachInSubtree[ast.FuncLit] is recursive, so all depths covered).
//     Reverse self-check: control_plane_exempt_func_closure_violates fixture
//     asserts that time.* inside a closure of a marked function IS flagged.
//
//  2. A method (receiver FuncDecl) declared inside a file-scope var init block:
//     not possible in Go syntax; not a blind spot.
//
// Uses FindFirstChild[ast.FuncDecl](file, ...) — top-level FuncDecls are
// direct children of *ast.File; no nested function literal can be a top-level
// FuncDecl, so depth=1 is correct.
//
// The closure-check in step 2 uses EachInSubtree outside any EachInChildren
// callback, so no sentinel flag is held inside an EachInChildren closure
// (SCANNER-FRAMEWORK-USAGE-02 compliant).
func enclosingFuncDeclKey(fset *token.FileSet, file *ast.File, pos token.Pos) string {
	// Step 1: find the top-level FuncDecl whose body spans pos. FindFirstChild
	// stops at depth=1 — correct because top-level FuncDecls are direct children
	// of *ast.File and there is at most one enclosing FuncDecl per pos.
	fd, ok := FindFirstChild[ast.FuncDecl](file, func(fd *ast.FuncDecl) bool {
		return fd.Body != nil && fd.Body.Pos() <= pos && pos <= fd.Body.End()
	})
	if !ok {
		return ""
	}
	// Step 2: reject pos if it falls inside a nested FuncLit body (closure).
	// EachInSubtree[ast.FuncLit] walks all FuncLits at any depth inside fd.Body.
	// The insideClosure flag is held outside any EachInChildren callback, so this
	// is not the USAGE-02 forbidden pattern (USAGE-02 only monitors
	// EachInChildren callbacks; this is a stand-alone EachInSubtree call).
	insideClosure := false
	EachInSubtree[ast.FuncLit](fd.Body, func(fl *ast.FuncLit) {
		if fl.Body != nil && fl.Body.Pos() <= pos && pos <= fl.Body.End() {
			insideClosure = true
		}
	})
	if insideClosure {
		return ""
	}
	return fset.Position(fd.Name.Pos()).String()
}

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
	// The rule func only populates ctors and always returns nil; _ = discards
	// the empty diagnostic slice intentionally (Phase 2 is the violation source).
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

// runClockCallsiteFixtureScan loads a fixture directory, scans test files for
// callsite violations, and asserts the diagnostic count matches the
// spec.Violation() markers declared inline in the fixture (via
// archtest.AssertDiagnosticCount).
func runClockCallsiteFixtureScan(t *testing.T, fixtureDir, ruleID string) {
	t.Helper()

	// Phase 1: collect ctors from the fixture module.
	// The rule func only populates ctors and always returns nil; _ = discards
	// the empty diagnostic slice intentionally (Phase 2 is the violation source).
	ctors := make(map[string]clockRequiredCtor)
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: true}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			for _, c := range collectClockRequiredCtorsFromPass(p) {
				ctors[c.ctorFullName] = c
			}
			return nil
		})

	// Phase 2: scan test files for callsite violations + assert count.
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: true}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !strings.HasSuffix(rel, "_test.go") {
					continue
				}
				d = append(d, scanClockCallsiteAST(p.Fset, f, rel, p.TypesInfo, ctors)...)
			}
			AssertDiagnosticCount(t, ruleID, p, d)
			return nil
		})
}

// TestClockInjectionCallsiteFixtures validates fixture-based regression cases.
// Expected violation counts are declared inline in each fixture via
// spec.Violation() calls; the helper asserts via AssertDiagnosticCount.
func TestClockInjectionCallsiteFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/clock_injection_callsite_fixtures"

	for _, pkg := range []string{"compliant", "violates"} {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()
			runClockCallsiteFixtureScan(t, base+"/"+pkg, "CLOCK-INJECTION-TEST-CALLSITE-01")
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
	// The rule func only populates ctors and always returns nil; _ = discards
	// the empty diagnostic slice intentionally (Phase 2 is the violation source).
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

// runLeafFallbackFixtureScan loads the fixture package at fixtureDir, scans
// for leaf-fallback violations, and asserts the diagnostic count matches the
// spec.Violation() markers declared inline in the fixture. The whitelist is
// intentionally NOT applied here — every fixture path is treated as
// production code so the gate's detection logic is the only thing under test.
func runLeafFallbackFixtureScan(t *testing.T, fixtureDir, ruleID string) {
	t.Helper()
	_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, scanLeafRealCallsAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			AssertDiagnosticCount(t, ruleID, p, d)
			return nil
		})
}

// TestKernelClockLeafFallbackFixtures runs the KERNEL-CLOCK-LEAF-FALLBACK-01
// scanner over each fixture subpackage and asserts the diagnostic count matches
// the spec.Violation() markers declared inline in each fixture's .go file.
// Mirrors TestProdClockInjectionFixtures (sibling gate).
func TestKernelClockLeafFallbackFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := root + "/tools/archtest/testdata/clock_leaf_fallback_fixtures"

	// "compliant": 0 violations (no spec.Violation() markers in fixture)
	// "violates":  3 violations (direct + alias + nil-fallback, each with
	//             a spec.Violation() marker inline in usage.go)
	for _, pkg := range []string{"compliant", "violates"} {
		pkg := pkg
		t.Run(pkg, func(t *testing.T) {
			t.Parallel()
			runLeafFallbackFixtureScan(t, base+"/"+pkg, "KERNEL-CLOCK-LEAF-FALLBACK-01")
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
// Expected violation counts are declared inline in each fixture via
// spec.Violation() calls; AssertDiagnosticCount enforces got==markers.
func TestKernelClockResetRelativeFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	fixturesBase := root + "/tools/archtest/testdata/clock_reset_relative_fixtures"

	cases := []struct {
		dir string
	}{
		// GREEN — compliant fixture expects 0 violations (no spec.Violation() in fixture).
		{"compliant"},
		// RED — expected diagnostic count declared via spec.Violation() in the fixture .go file.
		{"violates"},
	}

	for _, tc := range cases {
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := fixturesBase + "/" + tc.dir
			_ = RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
				func(p *Pass) []Diagnostic {
					var got []Diagnostic
					for _, f := range p.Files {
						rel := p.Rel(f)
						got = append(got, scanClockResetRelativeAST(p.Fset, f, rel, p.TypesInfo)...)
					}
					AssertDiagnosticCount(t, "KERNEL-CLOCK-RESET-RELATIVE-01", p, got)
					return nil
				})
		})
	}
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
// Function-level carve-out: a FuncDecl is exempt only if its doc comment
// contains `//archtest:allow:clock-injection:control-plane <reason>` (non-empty
// reason) AND its (module-relative path, func name) is in the
// controlPlaneClockCarveOut allowlist (review P1-3 — the marker alone is not
// sufficient). The exemption does NOT extend to other functions in the same
// file (incl. a third marked function) or to closures/FuncLits within the
// exempt FuncDecl body. Allowlisted carve-out functions:
//   - runtime/command/lifecycle.go: controlPlaneTicker, controlPlaneProbeTimer
//     (control-plane scheduling; backlog CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01)
//
// Registry: the carve-out ADR (docs/architecture/202605121800-adr-archtest-carveout-narrow.md)
// is scoped to ERRCODE-KIND-LITERAL-01 and does not govern clock carve-outs.
// Clock function-level carve-outs are self-documented here in the INVARIANT
// godoc + in the marker functions' own godoc. The in-source marker is the
// single enforcement truth source; this godoc is documentation only.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
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
// Function-level carve-out: if the forbidden reference sits inside a
// *ast.FuncDecl whose doc comment contains
// `//archtest:allow:clock-injection:control-plane <reason>` (non-empty reason
// required), that reference is exempt. The carve-out is function-level only —
// other functions in the same file without the marker are still checked.
//
// AI-rebust grade: Medium (comment guard). Hard upgrade path: backlog
// CONTROL-PLANE-CLOCK-TYPED-FUNNEL-HARD-UPGRADE-01.
//
// Blind spots (documented per ai-collab.md §"工具选定后强制盲区自检"):
//  1. Marker in a non-doc inline comment (e.g. // inside the function body):
//     NOT recognized — only doc comments (fd.Doc) are scanned.
//     Reverse self-check: control_plane_no_marker_violates fixture asserts
//     such a function IS flagged.
//  2. time.* inside a FuncLit/closure within an exempt FuncDecl: NOT exempt.
//     enclosingFuncDeclKey explicitly excludes positions inside nested FuncLit
//     bodies via EachInSubtree[ast.FuncLit], so closures are never granted
//     the FuncDecl-level carve-out.
//     Reverse self-check A: control_plane_closure_violates — a non-exempt
//     function's closure calling time.NewTicker is flagged.
//     Reverse self-check B: control_plane_exempt_func_closure_violates — a
//     closure inside a marked (exempt) function calling time.NewTicker is
//     still flagged (blind-spot-A closure-within-exempt-func self-check).
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
//
// scanProdClockInjectionAST is exported within the archtest package so the
// fixture-based regression tests in prod_clock_injection_fixtures_test.go can
// share the exact same predicate.
func scanProdClockInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	// Compute which FuncDecls are control-plane carve-out exempt: marker doc
	// comment AND (rel, name) ∈ controlPlaneClockCarveOut (review P1-3).
	allowedFuncs := clockControlPlaneAllowedFuncs(fset, file, rel)

	record := func(node ast.Node, name string) {
		// Function-level carve-out: skip if inside an exempt FuncDecl.
		if funcKey := enclosingFuncDeclKey(fset, file, node.Pos()); allowedFuncs[funcKey] {
			return
		}
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

// ---------------------------------------------------------------------------
// CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01
// ---------------------------------------------------------------------------

// INVARIANT: CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01
//
// TestControlPlaneClockCarveOutAllowlistIsLive asserts that every entry in
// controlPlaneClockCarveOut is live — i.e. not orphaned by a rename or deletion:
//
//	(a) The file at the module-relative path parses without error.
//	(b) Each listed function name exists as a top-level *ast.FuncDecl in that file.
//	(c) The FuncDecl's doc comment carries a valid clockControlPlaneAllowMarker
//	    with a non-empty reason.
//
// Without this guard, a rename of controlPlaneTicker or controlPlaneProbeTimer
// silently orphans the allowlist entry: enforcement keeps working (unknown names
// are never exempt), but the allowlist stops being a trustworthy record of what
// is actually exempt, violating ai-collab.md §"ADR amendment 落地必查" Medium
// anchor integrity.
//
// Detection strategy: pure go/parser AST scan (no types loading needed).
// EachInChildren[ast.FuncDecl](file, ...) at depth=1 finds top-level FuncDecls,
// which are direct children of *ast.File — correct and sufficient.
//
// Blind spots (AST-based, no type resolution):
//   - A method (receiver FuncDecl) could share the same name as a top-level
//     FuncDecl; EachInChildren[ast.FuncDecl] only reaches top-level declarations
//     (direct children of *ast.File), so receiver methods are never matched —
//     the check is correct by depth=1 constraint.
//   - The marker is required in the doc comment (fd.Doc), not an inline comment
//     inside the body. A function whose marker appears only in a body comment
//     would pass the carve-out allowlist check but fail PROD-CLOCK-INJECTION-01
//     (which uses the same doc-comment predicate). This test validates the same
//     predicate, so both sides agree.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
// ref: PROD-CLOCK-INJECTION-01 (clock_invariants_test.go clockControlPlaneAllowedFuncs)
func TestControlPlaneClockCarveOutAllowlistIsLive(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)

	// Collect sorted keys for deterministic error ordering.
	rels := make([]string, 0, len(controlPlaneClockCarveOut))
	for rel := range controlPlaneClockCarveOut {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		funcNames := controlPlaneClockCarveOut[rel]
		rel := rel // capture for t.Run
		t.Run(rel, func(t *testing.T) {
			t.Parallel()
			absPath := filepath.Join(root, filepath.FromSlash(rel))

			file, fset, err := parseCarveOutFile(absPath)
			assert.NoError(t, err,
				"CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01: %s: file must exist and parse; "+
					"update controlPlaneClockCarveOut if the file was renamed or deleted", rel)
			if err != nil {
				return
			}

			// Sort func names for deterministic sub-test order.
			names := sortedKeys(funcNames)
			for _, name := range names {
				name := name
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					checkCarveOutFuncDecl(t, fset, file, rel, name)
				})
			}
		})
	}
}

// parseCarveOutFile parses the Go source file at absPath using go/parser and
// returns the *ast.File and its *token.FileSet. It does not load type
// information — a pure AST parse is sufficient for allowlist liveness checks.
func parseCarveOutFile(absPath string) (*ast.File, *token.FileSet, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, absPath, nil, parser.ParseComments)
	if err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", absPath, err)
	}
	return file, fset, nil
}

// checkCarveOutFuncDecl asserts that funcName exists as a top-level *ast.FuncDecl
// in file AND that its doc comment carries a valid clockControlPlaneAllowMarker
// with a non-empty reason.
func checkCarveOutFuncDecl(t *testing.T, fset *token.FileSet, file *ast.File, rel, funcName string) {
	t.Helper()

	fd, found := findTopLevelFuncDecl(file, funcName)
	assert.True(t, found,
		"CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01: %s: top-level func %q not found; "+
			"update controlPlaneClockCarveOut or rename the allowlist entry", rel, funcName)
	if !found {
		return
	}

	hasMarker := funcDeclHasAllowMarker(fset, fd)
	assert.True(t, hasMarker,
		"CONTROL-PLANE-CARVEOUT-ALLOWLIST-LIVE-01: %s: func %q exists but its doc comment "+
			"does not carry a valid %q marker with a non-empty reason; "+
			"add the marker or remove the allowlist entry",
		rel, funcName, clockControlPlaneAllowMarker)
}

// findTopLevelFuncDecl returns the top-level *ast.FuncDecl named funcName and
// true if found; otherwise nil and false.
// Uses EachInChildren[ast.FuncDecl] (depth=1) because top-level FuncDecls are
// direct children of *ast.File — the same depth used by clockControlPlaneAllowedFuncs.
func findTopLevelFuncDecl(file *ast.File, funcName string) (*ast.FuncDecl, bool) {
	var result *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == funcName {
			result = fd
		}
	})
	return result, result != nil
}

// funcDeclHasAllowMarker reports whether fd's doc comment contains a valid
// clockControlPlaneAllowMarker with a non-empty reason. Mirrors the predicate
// in clockControlPlaneAllowedFuncs so both sides stay in sync.
func funcDeclHasAllowMarker(_ *token.FileSet, fd *ast.FuncDecl) bool {
	if fd.Doc == nil {
		return false
	}
	for _, c := range fd.Doc.List {
		text := strings.TrimSpace(c.Text)
		if !strings.HasPrefix(text, clockControlPlaneAllowMarker) {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(text, clockControlPlaneAllowMarker))
		if rest != "" {
			return true
		}
	}
	return false
}

// sortedKeys returns the keys of m sorted lexicographically.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
