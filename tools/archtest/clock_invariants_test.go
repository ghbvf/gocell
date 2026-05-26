// invariants:
//   - INVARIANT: CLOCK-INJECTION-PROD-CALLSITE-01
//   - INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//   - INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//   - INVARIANT: PROD-CLOCK-INJECTION-01
//   - INVARIANT: CLOCK-POSITIONAL-INJECTION-01
//
// Package archtest — clock injection invariants.
//
// Merged from:
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

	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

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

// callsWithClock reports whether any of the call arguments in parent contains
// a CallExpr whose callee's FullName matches withClockFullName.
func callsWithClock(parent *ast.CallExpr, info *types.Info, withClockFullName string) bool {
	for _, arg := range parent.Args {
		if _, ok := FindFirstInSubtree[ast.CallExpr](arg, func(call *ast.CallExpr) bool {
			fn := resolvedFunc(call.Fun, info)
			return fn != nil && fn.FullName() == withClockFullName
		}); ok {
			return true
		}
	}
	return false
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

// INVARIANT: CLOCK-INJECTION-PROD-CALLSITE-01
//
// TestClockInjectionProdCallsite enforces CLOCK-INJECTION-PROD-CALLSITE-01:
// production composition-root files (cmd/ + examples/*/main.go) must pass
// WithClock when calling constructors whose package exports WithClock.
//
// This is the production-side complement to test-side clock injection rules.
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
	var patterns []string
	for _, dir := range []string{"cmd", "examples"} {
		if compositionRootDirExists(filepath.Join(root, dir)) {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	if len(patterns) == 0 {
		t.Skip("no cmd/ or examples/ directories found")
	}

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

// scanClockCallsiteAST walks file looking for calls to any constructor in
// ctors and reports violations where WithClock is missing.
func scanClockCallsiteAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
	ctors map[string]clockRequiredCtor,
) []Diagnostic {
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
		// Only flag calls with at least one option argument.
		if len(call.Args) == 0 {
			return
		}
		if callsWithClock(call, info, ctor.withClockFullName) {
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
					callee.FullName(),
				),
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

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-LEAF-FALLBACK-01
// ---------------------------------------------------------------------------

// kernelClockPkgPath is the import path of the package whose Real() factory
// the gate guards.
const kernelClockPkgPath = "github.com/ghbvf/gocell/kernel/clock"

// allowedRealCallerPaths lists the production-code paths that may call
// kernel/clock.Real() directly.
var allowedRealCallerPaths = []string{
	"kernel/clock/clock.go",                 // Real() factory definition
	"cmd/corebundle/",                       // main composition root
	"cmd/gocell/",                           // gocell CLI composition root
	"gocell.go",                             // top-level entry
	"tests/e2e/internal/clients/clients.go", // e2e suite composition root
	"cells/accesscore/internal/testutil/",   // SessionRepoForTest / RealSessionRepo
	"cells/configcore/configcoretest/",      // BuildWriteService / BuildSubscribeService default clock
}

// INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//
// TestKernelClockLeafFallback enforces KERNEL-CLOCK-LEAF-FALLBACK-01:
// leaf-level clock.Real() construction is forbidden outside the composition root.
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
// violation Diagnostics for every call to kernel/clock.Real().
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
		if matchedKernelClockReal(info, e.Sel) {
			record(e)
		}
	})
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
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

// matchedKernelClockReal reports whether ident resolves to kernel/clock.Real.
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
// returns the sorted slice of violation Diagnostics.
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
// scanner over each fixture subpackage.
// Each fixture dir owns a diag.golden; GREEN fixtures have an empty golden.
func TestKernelClockLeafFallbackFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "clock_leaf_fallback_fixtures")

	dirs := []string{"compliant", "violates"}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			got := runLeafFallbackFixtureScan(t, base+"/"+dir)
			AssertGolden(t, filepath.Join(base, dir, "diag.golden"), got)
		})
	}
}

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-RESET-RELATIVE-PROD-01
// ---------------------------------------------------------------------------

// clockResetRelativeExemptPaths lists path prefixes exempt from the gate.
var clockResetRelativeExemptPaths = []string{
	"kernel/clock/clock.go",   // interface definition
	"kernel/clock/clockmock/", // fake implementation
}

// INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//
// TestKernelClockResetRelativeProd enforces KERNEL-CLOCK-RESET-RELATIVE-PROD-01:
// production code must use the absolute Timer.ResetAt(deadline time.Time) API.
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
// violation Diagnostics for every call `<expr>.Reset(d)`.
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
		if sig.Recv() == nil {
			return
		}
		if !isResetDurationBool(sig) {
			return
		}
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

// typeHasResetAt reports whether t exposes a method ResetAt(time.Time) bool.
func typeHasResetAt(t types.Type) bool {
	mset := types.NewMethodSet(t)
	sel := mset.Lookup(nil, "ResetAt")
	if sel == nil {
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

// TestKernelClockResetRelativeFixtures verifies the scanner against fixture packages.
func TestKernelClockResetRelativeFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "clock_reset_relative_fixtures")

	dirs := []string{"compliant", "violates"}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			got := runClockResetRelativeFixtureScan(t, base+"/"+dir)
			AssertGolden(t, filepath.Join(base, dir, "diag.golden"), got)
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

// allowedRealClockPaths lists the paths whose files may legitimately
// reference stdlib time symbols directly.
//
// Entry semantics: a trailing "/" marks a directory prefix; otherwise exact.
var allowedRealClockPaths = []string{
	"kernel/clock/",
	"pkg/testutil/testwait/testwait.go",
}

// forbiddenTimeFns maps each forbidden stdlib time function to the equivalent
// Clock interface method that production callers must use instead.
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

// clockControlPlaneAllowedMethods returns the set of FuncDecl name-positions
// (format: fset.Position(fd.Name.Pos()).String()) in file that are exempt from
// PROD-CLOCK-INJECTION-01 via receiver-type confinement.
//
// A FuncDecl is exempt if and only if ALL of:
//
//	(a) rel is under "runtime/command/" — this package gate prevents any other
//	    package from claiming to host a "controlPlaneClock" method; the type is
//	    package-private (unexported) so only code in runtime/command can declare
//	    methods on it. This is the structural "seal" that replaces the old
//	    hand-maintained allowlist map.
//
//	(b) fd.Recv != nil — it is a method, not a free function.
//
//	(c) The receiver's type name is "controlPlaneClock" (value receiver) or
//	    "*controlPlaneClock" (pointer receiver), extracted from the AST. Both
//	    forms are accepted for robustness, though the current impl uses value
//	    receivers only.
//
// Uses EachInChildren[ast.FuncDecl](file, ...) — top-level FuncDecls are
// direct children of *ast.File, so depth=1 is correct and sufficient.
//
// AI-robust grade: Medium. The stdlib time.NewTicker / time.NewTimer free
// functions cannot be made uncallable in Go, so receiver-type confinement is
// the permanent ceiling here (same as SPAN-SETATTR-REDACT-01 package-internal
// axis). The gain over the former comment-marker + allowlist-map form (#619):
//   - Eliminates the AI-abusable //archtest:allow:clock-injection:control-plane
//     marker that any production PR could add to any FuncDecl.
//   - Eliminates the hand-maintained controlPlaneClockCarveOut map.
//   - A new time.* callsite now requires adding a method to the sealed type,
//     which is a deliberate, reviewable code change (not a comment addition).
//
// Blind spots (per ai-robust.md §"工具选定后强制盲区自检"):
//  1. A FuncLit (anonymous function / closure) cannot be a method; time.* calls
//     inside closures within a controlPlaneClock method are NOT exempt.
//     enclosingFuncDeclKey explicitly excludes positions inside nested FuncLit
//     bodies via EachInSubtree[ast.FuncLit], so closures are never granted
//     the method-level carve-out.
//     Reverse self-check: control_plane_exempt_func_closure_violates fixture
//     asserts that time.* inside a closure of an exempt method is still flagged.
//  2. A method named controlPlaneClock from an entirely different package would
//     satisfy (b)+(c) without gate (a). Gate (a) prevents this by requiring
//     the file's module-relative path to be under runtime/command/.
//     Reverse self-check: control_plane_wrong_path_violates fixture has a struct
//     named controlPlaneClock with a method outside runtime/command/ → still flagged.
//  3. An unexported method on a different struct inside runtime/command/ with
//     the name "controlPlaneClock" is not a legitimate bypass because (c) checks
//     the *receiver type name*, not the method name. A struct named "otherClock"
//     with a method named "controlPlaneClock" is NOT exempt.
//     Reverse self-check: control_plane_wrong_receiver_type_violates asserts such
//     a method is flagged (receiver type "otherClock" ≠ "controlPlaneClock").
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
// ref: PROD-CLOCK-INJECTION-01
func clockControlPlaneAllowedMethods(fset *token.FileSet, file *ast.File, rel string) map[string]bool {
	out := map[string]bool{}
	// Gate (a): only runtime/command/ files can host controlPlaneClock methods.
	if !strings.HasPrefix(rel, "runtime/command/") {
		return out
	}
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		// Gate (b): must be a method (has receiver list).
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return
		}
		// Gate (c): receiver type name must be "controlPlaneClock" (or pointer to it).
		recvTypeName := clockReceiverTypeName(fd)
		if recvTypeName != "controlPlaneClock" {
			return
		}
		key := fset.Position(fd.Name.Pos()).String()
		out[key] = true
	})
	return out
}

// clockReceiverTypeName extracts the base type name of the first receiver in a
// FuncDecl's receiver list. Returns "" if none. Uses the shared ReceiverTypeName
// helper exported by the archtest package (walk.go). Renames the local helper to
// avoid conflict with the receiverTypeName function in pg_repo_ambient_tx_test.go.
func clockReceiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	return ReceiverTypeName(fd.Recv.List[0].Type)
}

// findTopLevelFuncDecl returns the top-level *ast.FuncDecl named funcName and
// true if found; otherwise nil and false.
// Uses EachInChildren[ast.FuncDecl] (depth=1) because top-level FuncDecls are
// direct children of *ast.File.
func findTopLevelFuncDecl(file *ast.File, funcName string) (*ast.FuncDecl, bool) {
	var result *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == funcName {
			result = fd
		}
	})
	return result, result != nil
}

// enclosingFuncDeclKey returns the position-string key for the nearest
// enclosing top-level *ast.FuncDecl that directly (not via a FuncLit/closure)
// contains pos. Returns "" if pos is not inside any FuncDecl body, or if pos
// is inside a nested FuncLit within a FuncDecl body.
//
// The key matches the format produced by clockControlPlaneAllowedMethods.
//
// Why the FuncLit exclusion matters (carve-out boundary):
//
// Without the exclusion, `fd.Body.Pos() <= pos <= fd.Body.End()` is true for
// any code inside the function — including closures/FuncLits. This would
// exempt `time.*` calls inside closures of an exempt method, which violates
// the documented carve-out semantics.
//
// Blind spots (per ai-robust.md §"工具选定后强制盲区自检"):
//
//  1. FuncLit nested inside another FuncLit inside an exempt FuncDecl: also
//     excluded (EachInSubtree[ast.FuncLit] is recursive, all depths covered).
//     Reverse self-check: control_plane_exempt_func_closure_violates fixture.
//
//  2. A method (receiver FuncDecl) declared inside a file-scope var init block:
//     not possible in Go syntax; not a blind spot.
//
// Uses FindFirstChild[ast.FuncDecl](file, ...) — top-level FuncDecls are
// direct children of *ast.File; depth=1 is correct.
func enclosingFuncDeclKey(fset *token.FileSet, file *ast.File, pos token.Pos) string {
	// Step 1: find the top-level FuncDecl whose body spans pos.
	fd, ok := FindFirstChild[ast.FuncDecl](file, func(fd *ast.FuncDecl) bool {
		return fd.Body != nil && fd.Body.Pos() <= pos && pos <= fd.Body.End()
	})
	if !ok {
		return ""
	}
	// Step 2: reject pos if it falls inside a nested FuncLit body (closure).
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
// Control-plane carve-out (receiver-type confinement, #619 upgrade):
// A FuncDecl is exempt from PROD-CLOCK-INJECTION-01 only if it is a METHOD
// whose receiver type name is "controlPlaneClock" AND the file's module-relative
// path is under "runtime/command/". This replaces the former comment-marker +
// hand-maintained allowlist-map form (which was AI-abusable: any marked
// FuncDecl in any allowlisted file could self-exempt by adding the comment).
//
// The exemption does NOT extend to closures/FuncLits within an exempt method
// body. enclosingFuncDeclKey explicitly rejects positions inside FuncLit nodes.
//
// AI-robust grade: Medium. Permanent ceiling: stdlib time.NewTicker/time.NewTimer
// free functions cannot be made uncallable in Go (same as SPAN-SETATTR-REDACT-01
// package-internal axis). Gain over prior form: eliminated AI-abusable comment
// marker and hand-maintained allowlist map (see clockControlPlaneAllowedMethods).
//
// Blind spots (documented per ai-robust.md §"工具选定后强制盲区自检"):
//  1. time.* inside a FuncLit/closure within an exempt method: NOT exempt.
//     enclosingFuncDeclKey excludes positions inside nested FuncLit bodies.
//     Reverse self-check A: control_plane_closure_violates — a non-exempt
//     function's closure calling time.NewTicker is flagged.
//     Reverse self-check B: control_plane_exempt_func_closure_violates — a
//     closure inside an exempt controlPlaneClock method is still flagged.
//  2. A struct named "controlPlaneClock" with methods outside runtime/command/:
//     the path gate prevents exemption.
//     Reverse self-check: control_plane_wrong_path_violates fixture.
//  3. A receiver type other than "controlPlaneClock" inside runtime/command/:
//     only the exact type name matches; wrong receiver type is NOT exempt.
//     Reverse self-check: control_plane_wrong_receiver_type_violates fixture.
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

// isAllowedRealClockPath reports whether rel is covered by one of the
// allowedRealClockPaths entries.
func isAllowedRealClockPath(rel string) bool {
	for _, p := range allowedRealClockPaths {
		if strings.HasSuffix(p, "/") {
			if strings.HasPrefix(rel, p) {
				return true
			}
		} else if rel == p {
			return true
		}
	}
	return false
}

// scanProdClockInjectionAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every reference to one of the forbidden stdlib
// time functions.
//
// Control-plane carve-out: if the forbidden reference sits inside a top-level
// *ast.FuncDecl that is a METHOD on receiver type "controlPlaneClock" AND the
// file's module-relative path is under "runtime/command/", that reference is
// exempt. The carve-out is method-level only — closures within the exempt
// method body are NOT exempt (enclosingFuncDeclKey rejects positions in FuncLit
// nodes). Other methods in the same file with a different receiver type are
// still checked.
//
// AI-robust grade: Medium (receiver-type confinement; see godoc of
// clockControlPlaneAllowedMethods for the permanent ceiling rationale).
//
// scanProdClockInjectionAST is exported within the archtest package so the
// fixture-based regression tests in prod_clock_injection_fixtures_test.go can
// share the exact same predicate.
func scanProdClockInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	// Receiver-type confinement: compute which FuncDecls in this file are exempt.
	// Only FuncDecls that are methods of controlPlaneClock AND whose file is
	// under runtime/command/ qualify.
	allowedFuncs := clockControlPlaneAllowedMethods(fset, file, rel)

	record := func(node ast.Node, name string) {
		// Carve-out: skip if inside an exempt FuncDecl (not inside a closure).
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
				name, forbiddenTimeFns[name],
			),
		})
	}

	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		if name, ok := matchedTimeFn(info, e.Sel); ok {
			record(e, name)
		}
	})
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
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

// matchedTimeFn reports whether ident resolves to a forbidden stdlib time function.
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
	// Exclude methods (e.g. time.Time.After) — only flag package-level functions.
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
// CLOCK-POSITIONAL-INJECTION-01
// ---------------------------------------------------------------------------

// mustHaveClockPkgPath is the import path of the package containing
// clock.MustHaveClock. Hard-coded so an import rename is loud (test breaks).
const mustHaveClockPkgPath = "github.com/ghbvf/gocell/kernel/clock"

// mustHaveClockFuncName is the function name in mustHaveClockPkgPath.
const mustHaveClockFuncName = "MustHaveClock"

// INVARIANT: CLOCK-POSITIONAL-INJECTION-01
//
// TestClockPositionalInjection enforces CLOCK-POSITIONAL-INJECTION-01:
// two sub-checks over production code (excludes _test.go, generated/,
// testdata/).
//
// Sub-check A: MustHaveClock positional-param enforcement.
// Every production call to clock.MustHaveClock(arg0, ...) must have arg0
// resolve (via go/types) to a PARAMETER of the enclosing function (a
// *types.Var that is in the enclosing FuncDecl's parameter list). Selector
// expressions like cfg.Clock, s.clock, c.clk, config.Clock are violations —
// they represent struct-field or variable sources, not injected parameters.
// Non-ident arg0 (selector expression, method call, etc.) → violation.
//
// The callee is resolved to kernel/clock.MustHaveClock via go/types so import
// aliases do not allow bypass.
//
// Sub-check B: exported WithClock ban.
// Any exported function named "WithClock" in a production package (a
// *ast.FuncDecl with Name.Name == "WithClock", exported, receiverless, in
// non-test non-generated files) is a violation. WithClock functions are option
// injectors that make clock injection optional/omittable. The positional
// injection pattern makes the clock a mandatory parameter instead.
//
// Both sub-checks are EXPECTED TO FAIL against the current tree (RED state):
//   - Sub-check A: ~24 violations where constructors still use
//     option/struct-field clock injection via MustHaveClock(cfg.Clock, ...) or
//     MustHaveClock(s.clk, ...) forms.
//   - Sub-check B: ~14 exported WithClock functions that have not yet been
//     deleted as part of the positional-param migration.
//
// These RED violations are the conversion worklist for the subsequent migration
// waves. The archtest is written correctly to report violations — do NOT
// suppress them. The violations will be driven to zero as each constructor is
// migrated to accept a positional clock.Clock parameter.
//
// AI-robust grading:
//
//   - Downstream Hard: this archtest locks the form (any MustHaveClock call
//     with a non-param arg0 is a violation; any exported WithClock function is
//     a violation). Once the migration is complete, regression to the old pattern
//     is caught immediately.
//
//   - Upstream Hard: the Go compiler is the upstream enforcement. A mandatory
//     positional `clk clock.Clock` parameter makes omission a compile error —
//     that is the compiler-enforced presence guarantee. This archtest only
//     ensures the FORM stays positional (arg0 is a parameter, not a
//     field/variable) so presence can never regress to an omittable option.
//
// Blind spots (per ai-robust.md §"工具选定后强制盲区自检"):
//  1. Callee resolution: if MustHaveClock is invoked through an interface
//     method or function value (not a direct qualified ident), it may not
//     resolve to the canonical *types.Func. In practice MustHaveClock is always
//     called as clock.MustHaveClock (or aliased import). The callee is checked
//     via types.Info.ObjectOf on the call's Fun Sel, so aliases are handled.
//     Reverse self-check: fixture uses aliased import and confirms it is still
//     detected.
//  2. Multi-return enclosing function: param lookup scans all params in the
//     enclosing FuncDecl's type including variadic params. A *types.Var that
//     IS a param but is being passed via a selector expression (e.g. params.Clock
//     where params is itself a param struct) would have a non-ident arg0 →
//     violation. This is the intended behavior: params.Clock is a field access,
//     not a direct parameter.
//  3. Closures / anonymous function parameters: if MustHaveClock is called
//     inside a closure, the enclosing FuncDecl is found via FindFirstChild (same
//     depth=1 constraint as enclosingFuncDeclKey), not the inner FuncLit. A
//     closure parameter (not a top-level FuncDecl param) passing its own local
//     clock would be flagged. Accepted: closures wrapping constructors must pass
//     the param down explicitly.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D
func TestClockPositionalInjection(t *testing.T) {
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
				d = append(d, scanClockPositionalInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})

	Report(t, "CLOCK-POSITIONAL-INJECTION-01", diags)
}

// scanClockPositionalInjectionAST runs both sub-checks A and B over a single
// file and returns all violations, sorted by line.
func scanClockPositionalInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	// Sub-check A: MustHaveClock arg0 must be a parameter.
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !isMustHaveClockCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		arg0 := call.Args[0]
		if !clockArg0IsParam(file, arg0, info) {
			line := fset.Position(call.Pos()).Line
			key := fmt.Sprintf("%s:%d:A", rel, line)
			if !seen[key] {
				seen[key] = true
				out = append(out, Diagnostic{
					Rel:  rel,
					Line: line,
					Message: "clock.MustHaveClock: arg0 must be a function parameter (positional injection); " +
						"selector or non-ident arg0 (e.g. cfg.Clock, s.clk) violates the positional-param contract. " +
						"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md",
				})
			}
		}
	})

	// Sub-check B: ban exported WithClock free functions.
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name != "WithClock" {
			return
		}
		// Must be exported (WithClock is uppercase — always exported by Go rules).
		if !fd.Name.IsExported() {
			return
		}
		// Must be a free function (no receiver).
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			return
		}
		line := fset.Position(fd.Name.Pos()).Line
		key := fmt.Sprintf("%s:%d:B", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "exported WithClock function found — WithClock option injectors are banned by " +
					"CLOCK-POSITIONAL-INJECTION-01; migrate to a mandatory positional clock.Clock parameter. " +
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

// isMustHaveClockCall reports whether call is a call to kernel/clock.MustHaveClock.
// Resolution is type-driven via go/types.Info so import aliases do not bypass.
func isMustHaveClockCall(call *ast.CallExpr, info *types.Info) bool {
	if info == nil {
		return false
	}
	fn := resolvedFunc(call.Fun, info)
	if fn == nil {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != mustHaveClockPkgPath {
		return false
	}
	return fn.Name() == mustHaveClockFuncName
}

// clockArg0IsParam reports whether arg0 (the first argument to MustHaveClock)
// is a direct reference to a parameter of the enclosing top-level FuncDecl.
//
// Returns false (violation) if:
//   - arg0 is not an *ast.Ident (e.g. selector cfg.Clock, method call, etc.)
//   - arg0 is an *ast.Ident but it does not resolve to a *types.Var
//   - arg0 resolves to a *types.Var that is NOT in the enclosing FuncDecl's
//     parameter list (i.e. it is a struct field, local variable, package-level
//     var, etc.)
//
// Uses FindFirstChild[ast.FuncDecl] at depth=1 to find the enclosing top-level
// FuncDecl — the same depth constraint as enclosingFuncDeclKey.
func clockArg0IsParam(file *ast.File, arg0 ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	// arg0 must be a bare identifier (not a selector, index, call, etc.)
	ident, ok := arg0.(*ast.Ident)
	if !ok {
		return false
	}
	// Resolve to a *types.Var.
	obj, ok := info.ObjectOf(ident).(*types.Var)
	if !ok {
		return false
	}
	// Find the enclosing top-level FuncDecl.
	fd, found := FindFirstChild[ast.FuncDecl](file, func(fd *ast.FuncDecl) bool {
		return fd.Body != nil && fd.Body.Pos() <= arg0.Pos() && arg0.End() <= fd.Body.End()
	})
	if !found {
		return false
	}
	// Collect all parameters from the FuncDecl's signature (including receiver
	// treated as a param? NO — receiver is not a param; only Params list).
	if fd.Type == nil || fd.Type.Params == nil {
		return false
	}
	return funcDeclHasParam(fd, obj, info)
}

// funcDeclHasParam reports whether obj is one of the parameters declared in
// fd's parameter list. Comparison is done via *types.Var pointer identity —
// the same object resolved by info.ObjectOf in the declaration site.
//
// We iterate the AST field list and call info.ObjectOf on each param name
// ident to get the canonical *types.Var. We then compare by pointer identity
// (or position as a fallback) against obj.
func funcDeclHasParam(fd *ast.FuncDecl, obj *types.Var, info *types.Info) bool {
	if fd.Type == nil || fd.Type.Params == nil {
		return false
	}
	for _, field := range fd.Type.Params.List {
		for _, nameIdent := range field.Names {
			paramVar, ok := info.ObjectOf(nameIdent).(*types.Var)
			if !ok {
				continue
			}
			if paramVar == obj {
				return true
			}
			// Fallback: compare by position — handles edge cases where the
			// types.Info returns different *types.Var instances for the same
			// underlying variable across load variants.
			if paramVar.Pos() == obj.Pos() {
				return true
			}
		}
	}
	return false
}

// runClockPositionalInjectionFixtureScan loads the fixture package at fixtureDir
// and returns the sorted slice of violation Diagnostics using the same predicate
// as TestClockPositionalInjection.
func runClockPositionalInjectionFixtureScan(t *testing.T, fixtureDir string) []Diagnostic {
	t.Helper()
	return RunTypedDir(t, fixtureDir, TypedOpts{Tests: false}, []string{"./..."},
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				d = append(d, scanClockPositionalInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// TestClockPositionalInjectionFixtures runs CLOCK-POSITIONAL-INJECTION-01
// over each fixture subpackage and asserts against the golden.
// GREEN fixtures have an empty golden; RED fixtures capture the expected output.
func TestClockPositionalInjectionFixtures(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based fixture test in -short mode")
	}

	root := findModuleRoot(t)
	base := filepath.Join(root, "tools", "archtest", "testdata", "clock_positional_injection_fixtures")

	// GREEN: compliant (arg0 is a param; no WithClock).
	// RED: violations (selector arg0; exported WithClock).
	dirs := []string{
		"param_passes",       // GREEN: MustHaveClock(clk, ...) where clk is a param
		"selector_violates",  // RED: MustHaveClock(cfg.Clock, ...) — selector arg0
		"withclock_violates", // RED: exported func WithClock(...)
	}

	for _, dir := range dirs {
		dir := dir
		t.Run(dir, func(t *testing.T) {
			t.Parallel()
			fixtureDir := filepath.Join(base, dir)
			diags := runClockPositionalInjectionFixtureScan(t, fixtureDir)
			AssertGolden(t, filepath.Join(fixtureDir, "diag.golden"), diags)
		})
	}
}
