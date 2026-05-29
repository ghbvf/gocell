// invariants:
//   - INVARIANT: KERNEL-CLOCK-LEAF-FALLBACK-01
//   - INVARIANT: KERNEL-CLOCK-RESET-RELATIVE-PROD-01
//   - INVARIANT: PROD-CLOCK-INJECTION-01 (control-plane hosts: runtime/command/
//   - kernel/reconcile/; the kernel/reconcile host realizes
//     RECONCILE-LOOP-CLOCK-CARVEOUT-01)
//   - INVARIANT: CLOCK-POSITIONAL-INJECTION-01
//
// Package archtest — clock injection invariants.
//
// Merged from:
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
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/fileroles"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
)

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-LEAF-FALLBACK-01
// ---------------------------------------------------------------------------

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

// controlPlaneClockCarveOut maps each sanctioned control-plane host package
// (directory prefix, trailing "/") to the exact set of (controlPlaneClock method
// name → stdlib time.* function name, without the "time." prefix) pairs that
// host's methods may call directly. The carve-out key is the (host, method,
// callee) TRIPLE: a method is exempt ONLY in the host that declares it.
//
// Why host-scoped, not a global method→callee map (#1275 review F1): a flat,
// host-agnostic map let any host borrow any other host's method exception — and
// let a newly added host inherit ALL methods for free. Binding the method set to
// its host closes both: runtime/command's newTicker is not exempt in
// kernel/reconcile, kernel/reconcile's newRequeueTimer/now are not exempt in
// runtime/command, and adding a third host grants NOTHING until that host gets
// its own explicit (method→callee) entries here.
//
// Host prefixes are disjoint (runtime/command/ vs kernel/reconcile/), so the
// per-rel lookup in controlPlaneClockHostMethods is unambiguous.
//
// Extension policy (HARD form-uniqueness): adding a new exempt callsite requires
// BOTH a deliberate entry here under the owning host AND a matching method on
// that host's package-private controlPlaneClock type. A method without an entry
// is a violation for every time.* call it makes; an entry without a matching
// method binds to no FuncDecl position and does nothing. Both halves are
// required, and the host that declares the method must be the host that lists
// it.
//
// AI-robust grade unchanged: Medium (permanent ceiling) — see
// clockControlPlaneAllowedMethods. Adding a host/method is a deliberate,
// reviewable change here; the per-host seal is the package-private type name +
// gate (c) + this host-scoped table.
//
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md §#619
var controlPlaneClockCarveOut = map[string]map[string]string{
	"runtime/command/": {
		"newTicker":     "NewTicker", // SweeperLifecycle ticker
		"newProbeTimer": "NewTimer",  // startup probe
	},
	"kernel/reconcile/": {
		"newProbeTimer":   "NewTimer", // startup probe
		"newRequeueTimer": "NewTimer", // delayed requeue (RECONCILE-LOOP-CLOCK-CARVEOUT-01)
		"now":             "Now",      // reconcile-duration measurement
	},
}

// controlPlaneClockHostMethods returns the (method name → sanctioned callee) set
// for the control-plane host package that rel belongs to, or nil if rel is under
// no sanctioned host (gate (a)). Host prefixes in controlPlaneClockCarveOut are
// disjoint, so at most one matches.
func controlPlaneClockHostMethods(rel string) map[string]string {
	for host, methods := range controlPlaneClockCarveOut {
		if strings.HasPrefix(rel, host) {
			return methods
		}
	}
	return nil
}

// clockControlPlaneAllowedMethods returns the map of FuncDecl name-positions
// (format: fset.Position(fd.Name.Pos()).String()) to the sanctioned stdlib
// callee for methods in file that are candidates for the PROD-CLOCK-INJECTION-01
// carve-out.
//
// A FuncDecl is a candidate if and only if ALL of:
//
//	(a) rel is under a sanctioned control-plane host package — i.e.
//	    controlPlaneClockHostMethods(rel) is non-nil (host keys of
//	    controlPlaneClockCarveOut: "runtime/command/" or "kernel/reconcile/").
//	    This package gate prevents any other package from claiming to host a
//	    "controlPlaneClock" method; the type is package-private (unexported) so
//	    only code in a host package can declare methods on it. This is the
//	    structural "seal" that replaces the old hand-maintained allowlist map.
//
//	(b) fd.Recv != nil — it is a method, not a free function.
//
//	(c) The receiver's type name is "controlPlaneClock" (value receiver) or
//	    "*controlPlaneClock" (pointer receiver), extracted from the AST. Both
//	    forms are accepted for robustness, though the current impl uses value
//	    receivers only.
//
//	(d) The method name is listed FOR THIS HOST in controlPlaneClockCarveOut.
//	    A method listed only under a different host is NOT a candidate here (no
//	    cross-host borrowing — #1275 review F1); a method absent from every host
//	    set is not a candidate either, closing the "any time.* in any method
//	    body" gap (#1136 review F2).
//
// The returned map value is the sanctioned stdlib callee for that (host, method)
// pair; callers check the resolved time function name against it to decide
// whether the specific time.* call is sanctioned (exact (host, method, callee)
// triple).
//
// Uses EachInChildren[ast.FuncDecl](file, ...) — top-level FuncDecls are
// direct children of *ast.File, so depth=1 is correct and sufficient.
//
// AI-robust grade: Medium (permanent ceiling). The stdlib time.NewTicker /
// time.NewTimer free functions cannot be made uncallable in Go, so receiver-type
// confinement is the permanent ceiling here (same as SPAN-SETATTR-REDACT-01
// package-internal axis). Form-uniqueness within that ceiling is now the
// (host, method, callee) exact-triple — the strongest available form for stdlib
// free-function callouts. Gain over the former receiver-type-only carve-out
// (#1136 F2) and the host-agnostic method map (#1275 F1):
//   - Any time.* call inside a controlPlaneClock method body other than the
//     declared (host, method, callee) triple is a violation — no "method body
//     wildcard".
//   - Adding a controlPlaneClock method without a controlPlaneClockCarveOut
//     entry for its host leaves it ungated; any time.* call in it is flagged.
//   - A host cannot use another host's sanctioned method, and a newly added
//     host inherits no exceptions until it gets its own explicit entries.
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
//     satisfy (b)+(c) without gate (a). Gate (a) prevents this by requiring the
//     file's module-relative path to be under a sanctioned host package
//     (controlPlaneClockCarveOut keys: runtime/command/ or kernel/reconcile/).
//     Reverse self-check: control_plane_wrong_path_violates fixture has a struct
//     named controlPlaneClock with a method outside any host → still flagged.
//     GREEN self-check for the kernel/reconcile host: control_plane_reconcile_passes.
//  3. An unexported method on a different struct inside runtime/command/ with
//     the name "controlPlaneClock" is not a legitimate bypass because (c) checks
//     the *receiver type name*, not the method name. A struct named "otherClock"
//     with a method named "controlPlaneClock" is NOT exempt.
//     Reverse self-check: control_plane_wrong_receiver_type_violates asserts such
//     a method is flagged (receiver type "otherClock" ≠ "controlPlaneClock").
//  4. A new controlPlaneClock method (e.g. "harvest") that calls time.Sleep
//     would have passed under the receiver-type-only form; the host-scoped table
//     rejects it because "harvest" is in no host's set.
//     Reverse self-check: control_plane_wrong_method_name_violates fixture.
//  5. A sanctioned method (e.g. "newTicker") that calls the wrong time.*
//     function (e.g. time.Sleep instead of time.NewTicker) would pass under
//     the receiver-type-only form; the stored-callee check rejects it because
//     the host's "newTicker" callee is "NewTicker", not "Sleep".
//     Reverse self-check: control_plane_wrong_callee_violates fixture.
//  6. A host using ANOTHER host's sanctioned method (e.g. kernel/reconcile
//     declaring "newTicker", which is only runtime/command's) would have passed
//     under the former host-agnostic method map; the host-scoped table rejects
//     it because "newTicker" is not in the kernel/reconcile set (#1275 F1).
//     Reverse self-check: control_plane_cross_host_method_violates fixture.
//
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md §#619
// ref: PROD-CLOCK-INJECTION-01
func clockControlPlaneAllowedMethods(fset *token.FileSet, file *ast.File, rel string) map[string]string {
	out := map[string]string{}
	// Gate (a): resolve the host's (method→callee) carve-out set. nil ⇒ rel is
	// under no sanctioned control-plane host, so no method here is a candidate.
	hostMethods := controlPlaneClockHostMethods(rel)
	if hostMethods == nil {
		return out
	}
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		// Gate (b): must be a method (has receiver list).
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return
		}
		// Gate (c): receiver type name must be "controlPlaneClock" (or pointer to it).
		if clockReceiverTypeName(fd) != "controlPlaneClock" {
			return
		}
		if fd.Name == nil {
			return
		}
		// Gate (d): the method must be listed FOR THIS HOST. A method present in a
		// different host's set is NOT a candidate here (the host-scoped table
		// prevents cross-host borrowing — #1275 review F1). Methods absent from
		// every host set are NOT candidates either, closing the "any method body
		// wildcard" loophole (#1136 review F2). The stored value is the sanctioned
		// stdlib callee, so the caller checks the exact (host, method, callee)
		// triple without a second lookup.
		callee, ok := hostMethods[fd.Name.Name]
		if !ok {
			return
		}
		out[fset.Position(fd.Name.Pos()).String()] = callee
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
// Control-plane carve-out (host-scoped (host, method, callee) triple, #619 +
// #1275 F1): a FuncDecl is exempt from PROD-CLOCK-INJECTION-01 only if it is a
// METHOD whose receiver type name is "controlPlaneClock", whose file is under a
// sanctioned host (controlPlaneClockCarveOut keys: runtime/command/ or
// kernel/reconcile/), AND whose name maps to the exact stdlib callee listed for
// THAT host. This replaces the former comment-marker + hand-maintained
// allowlist-map form (which was AI-abusable: any marked FuncDecl in any
// allowlisted file could self-exempt by adding the comment).
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
//  2. A struct named "controlPlaneClock" with methods outside any sanctioned
//     host: the path gate prevents exemption.
//     Reverse self-check: control_plane_wrong_path_violates fixture.
//  3. A receiver type other than "controlPlaneClock" inside a host: only the
//     exact type name matches; wrong receiver type is NOT exempt.
//     Reverse self-check: control_plane_wrong_receiver_type_violates fixture.
//  4. A host using another host's sanctioned method (e.g. kernel/reconcile
//     declaring runtime/command's "newTicker"): the host-scoped table denies it.
//     Reverse self-check: control_plane_cross_host_method_violates fixture.
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md
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
// Control-plane carve-out: a forbidden reference is exempt only if it sits in a
// top-level *ast.FuncDecl that is a METHOD on receiver type "controlPlaneClock",
// in a file under a sanctioned host (controlPlaneClockCarveOut keys:
// runtime/command/ or kernel/reconcile/), AND the method/callee match the exact
// (host, method, callee) triple listed for that host. The carve-out is
// method-level only — closures within the exempt method body are NOT exempt
// (enclosingFuncDeclKey rejects positions in FuncLit nodes). Other methods in
// the same file with a different receiver type are still checked.
//
// AI-robust grade: Medium ((host, method, callee) triple confinement; see godoc
// of clockControlPlaneAllowedMethods for the permanent ceiling rationale).
//
// scanProdClockInjectionAST is exported within the archtest package so the
// fixture-based regression tests in prod_clock_injection_fixtures_test.go can
// share the exact same predicate.
func scanProdClockInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	// Receiver-type + (host, method, callee) confinement: compute which FuncDecls
	// in this file may host a sanctioned time.* call, mapped to the sanctioned
	// stdlib callee for that (host, method) pair. Only FuncDecls that are methods
	// of controlPlaneClock AND whose file is under a sanctioned host AND whose
	// name is listed for THAT host (controlPlaneClockCarveOut) qualify. The final
	// callee check happens inside record() against the stored callee.
	allowedFuncs := clockControlPlaneAllowedMethods(fset, file, rel)

	record := func(node ast.Node, name string) {
		// (host, method, callee) exact-triple carve-out: skip only if pos is
		// inside an exempt FuncDecl (not inside a closure) AND the time.* function
		// name matches the sanctioned callee stored for that method/host.
		if funcKey := enclosingFuncDeclKey(fset, file, node.Pos()); funcKey != "" {
			if sanctionedCallee, isCandidate := allowedFuncs[funcKey]; isCandidate {
				if sanctionedCallee == name {
					return // sanctioned (host, method, callee) triple
				}
				// fall through: method is a carve-out candidate but the callee
				// does not match the declared pair — record violation.
			}
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
// testdata/, and recognized test-helper packages such as configcoretest/,
// auditcoretest/, accesscoretest/).
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
// Sub-check B: clock option-injector ban (broadened).
// Any exported free function (no receiver) in a production package is a
// violation if it satisfies ALL of the following precise typed predicates:
//
//	(1) name contains "Clock" (catches WithClock, WithEnvClock, WithRouterClock,
//	    WithServiceTokenClock, etc. — any suffixed variant)
//	(2) parameter list includes a kernel/clock.Clock-typed parameter (resolved
//	    via go/types so aliases cannot bypass; the clock param need not be
//	    first — the check scans the full parameter list)
//	(3) return type is a function type OR a named type whose underlying type is a
//	    function (the functional-option shape, e.g. Option = func(*T), EnvOption
//	    = func(*cfg)) — this prevents false-flagging constructors and helpers
//	    that also contain "Clock" in their name.
//
// All three predicates must hold simultaneously. Non-receiver functions that
// contain "Clock" but do NOT take clock.Clock and return an option are NOT
// flagged (e.g. clock.MustHaveClock itself, utility functions).
//
// Sub-check C: input Config/Options struct field ban (#1136 review F1).
// Any EXPORTED type declaration whose name has the suffix "Config" / "Options"
// / "Opts" AND which has an EXPORTED struct field whose type resolves (via
// go/types) to kernel/clock.Clock is a violation. The closed naming convention
// {Config, Options, Opts} is the framework's input-struct surface — declaring
// an exported Clock field on such a struct lets external callers omit it via
// struct literal omission (zero-value Clock = nil interface), bypassing the
// positional parameter promise of CLOCK-POSITIONAL-INJECTION-01.
//
// Predicates (all must hold):
//
//	(1) the TypeSpec name is exported (capitalized) AND has suffix "Config",
//	    "Options", or "Opts" — exported input-struct naming convention
//	(2) the underlying type is a struct
//	(3) at least one EXPORTED field has type resolving to kernel/clock.Clock
//	    (via go/types — aliases cannot bypass)
//
// Carve-outs (by design, not by hand-maintained allowlist):
//
//   - Composition-root holders such as cmd/corebundle.SharedDeps (single source
//     where clock.Real() enters per ADR 202605270000 §Decision #4) do not match
//     the Config/Options/Opts suffix, so they are excluded by naming convention.
//   - Unexported option-pattern accumulator structs (e.g. runtime/auth.authConfig,
//     runtime/auth.serviceTokenConfig, runtime/config.watcherConfig) hold the
//     clock as an unexported field and are constructed only inside their own
//     package by `defaultFooConfig()` + WithFoo option loop; the positional
//     `clk clock.Clock` parameter on the public constructor assigns into the
//     internal field. External callers cannot construct the struct directly.
//     These are excluded because the field is unexported.
//   - Unexported config structs with exported Clock fields (e.g. dispatcherConfig
//     in kernel/assembly/hook_dispatcher.go before the dead-field cleanup) are
//     also excluded: the struct cannot be constructed outside its package, so
//     external callers cannot omit the clock via struct literal. Same-package
//     dead-field hygiene is handled as a separate code-review concern.
//
// AI-robust grade: Medium downstream (this archtest is type-aware struct-field
// + name suffix + exported-name lock). Upstream Hard is the Go compiler: with
// no exported Clock field on the input struct, a callsite like `Cfg{Clock: nil}`
// produces a compile error (unknown field), and the positional parameter is
// mandatory by signature.
//
// Test-helper packages (configcoretest/, auditcoretest/, accesscoretest/, etc.)
// are classified as test code by fileroles.IsProductionCode and are therefore
// excluded. Their With*Clock builders (e.g. configcoretest.WithWriteClock,
// auditcoretest.WithChainClock, accesscoretest.WithIdentityClock) intentionally
// use the option pattern for test-infra ergonomics; the production constructors
// they wrap are already positional.
//
// Both sub-checks are now GREEN against the current tree (zero violations).
// The archtest is the downstream-Hard gate: any regression to the old pattern
// (option injector or non-param arg0) is caught immediately at PR time.
//
// AI-robust grading:
//
//   - Downstream Hard: this archtest locks the form (any MustHaveClock call
//     with a non-param arg0 is a violation; any exported clock-option-injector
//     function is a violation). Once the migration is complete, regression to
//     the old pattern is caught immediately.
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
//     Reverse self-check: aliased_import_selector_violates fixture confirms
//     that `import clk "...kernel/clock"; clk.MustHaveClock(cfg.Clock, ...)`
//     is still detected as a violation.
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
//  4. Sub-check B relies on go/types for clock.Clock param resolution. A
//     function that takes an interface type with the same method set as
//     clock.Clock but defined in a different package would not be flagged —
//     the check is exact package-path comparison. Accepted: all production
//     clock injection must use kernel/clock.Clock per architecture rules.
//  5. Sub-check B return-type check uses types.Signature.Results().At(0) and
//     inspects the underlying type. A multi-return function is not a functional
//     option and is not flagged — multi-return clock constructors are intentional
//     (error-first pattern). A function returning (Option, error) is checked only
//     on the first return: if the first return is a function type the check fires.
//     Accepted: (Option, error) constructors are banned for the same reason as
//     single-return options — they still make clock injection omittable.
//
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md
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

// scanClockPositionalInjectionAST runs all three sub-checks (A, B, C) over a
// single file and returns all violations, sorted by line.
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
						"ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md",
				})
			}
		}
	})

	// Sub-check B: ban exported clock option-injector free functions.
	// Predicate (all three must hold):
	//   (1) name contains "Clock" (catches WithClock, WithEnvClock, WithRouterClock, etc.)
	//   (2) parameter list includes a kernel/clock.Clock-typed param (type-resolved)
	//   (3) return type is a function/option type (functional-option shape)
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil {
			return
		}
		// (1) name must contain "Clock"
		if !strings.Contains(fd.Name.Name, "Clock") {
			return
		}
		// Must be exported.
		if !fd.Name.IsExported() {
			return
		}
		// Must be a free function (no receiver).
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			return
		}
		// (2) parameter list must include a kernel/clock.Clock-typed param.
		if !clockOptionInjectorHasClockParam(fd, info) {
			return
		}
		// (3) return type must be a function/option type (functional-option shape).
		if !clockOptionInjectorReturnsOption(fd, info) {
			return
		}
		line := fset.Position(fd.Name.Pos()).Line
		key := fmt.Sprintf("%s:%d:B", rel, line)
		if !seen[key] {
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"exported clock option-injector %q found — With*Clock option injectors are banned by "+
						"CLOCK-POSITIONAL-INJECTION-01; migrate to a mandatory positional clock.Clock parameter. "+
						"ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md",
					fd.Name.Name,
				),
			})
		}
	})

	// Sub-check C: input Config/Options struct field ban.
	// Predicates (all must hold):
	//   (1) TypeSpec name is exported AND has suffix Config/Options/Opts (the
	//       framework's externally-constructable input-struct naming convention)
	//   (2) underlying type is *ast.StructType
	//   (3) at least one EXPORTED field has type resolving to kernel/clock.Clock
	//       (via go/types — unexported fields cannot be set via struct literal
	//       from external packages, so they cannot bypass positional injection)
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if ts.Name == nil || !ts.Name.IsExported() || !isInputConfigStructName(ts.Name.Name) {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !structFieldIsKernelClock(field, info) {
				continue
			}
			// Sub-check C predicate (3): only exported fields permit external
			// caller bypass via struct literal. Anonymous (embedded) fields are
			// exported when the embedded type name is exported.
			if !structFieldIsExported(field) {
				continue
			}
			// Field with a single name is the common shape (`Clock clock.Clock`);
			// rare anonymous fields are reported against the type expression line.
			reportPos := field.Type.Pos()
			fieldName := "<anonymous>"
			if len(field.Names) > 0 {
				reportPos = field.Names[0].Pos()
				fieldName = field.Names[0].Name
			}
			line := fset.Position(reportPos).Line
			key := fmt.Sprintf("%s:%d:C", rel, line)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: fmt.Sprintf(
					"input Config/Options struct %s declares exported %s field of type clock.Clock; "+
						"clock must enter via positional parameter, not struct literal — "+
						"struct-field injection lets callers omit the clock at compile time. "+
						"ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md",
					ts.Name.Name, fieldName,
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

// isInputConfigStructName reports whether name matches the framework's input
// Config/Options struct naming convention used by sub-check C.
//
// The convention is a closed suffix set: Config, Options, Opts. This is the
// HARD form-uniqueness lock that obviates a hand-maintained allowlist —
// composition-root holders such as cmd/corebundle.SharedDeps (the single
// sanctioned threading source per ADR 202605270000 §Decision #4) naturally
// fall outside the suffix set and are exempt without explicit carve-out.
func isInputConfigStructName(name string) bool {
	return strings.HasSuffix(name, "Config") ||
		strings.HasSuffix(name, "Options") ||
		strings.HasSuffix(name, "Opts")
}

// structFieldIsExported reports whether field has at least one exported name,
// or — for anonymous (embedded) fields — whether the embedded type's terminal
// identifier is exported. This mirrors Go's own export rules for struct
// literal field accessibility from external packages.
func structFieldIsExported(field *ast.Field) bool {
	if field == nil {
		return false
	}
	if len(field.Names) > 0 {
		for _, name := range field.Names {
			if name.IsExported() {
				return true
			}
		}
		return false
	}
	// Anonymous field: terminal ident determines the implicit field name.
	ident, ok := typeExprToIdent(field.Type)
	if !ok {
		return false
	}
	return ident.IsExported()
}

// structFieldIsKernelClock reports whether field declares at least one
// kernel/clock.Clock-typed identifier (resolved via go/types). The check
// uses the first name's *types.Var for typed fields; anonymous fields fall
// back to AST identifier resolution via typeExprToIdent.
func structFieldIsKernelClock(field *ast.Field, info *types.Info) bool {
	if info == nil || field == nil {
		return false
	}
	if len(field.Names) > 0 {
		obj, ok := info.ObjectOf(field.Names[0]).(*types.Var)
		if !ok {
			return false
		}
		return isKernelClockType(obj.Type())
	}
	// Anonymous (embedded) field — inspect the type expression directly.
	ident, ok := typeExprToIdent(field.Type)
	if !ok {
		return false
	}
	obj, ok := info.ObjectOf(ident).(*types.TypeName)
	if !ok {
		return false
	}
	return isKernelClockType(obj.Type())
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

// clockOptionInjectorHasClockParam reports whether fd's parameter list includes
// a parameter of type kernel/clock.Clock (resolved via go/types). This is
// predicate (2) of sub-check B in CLOCK-POSITIONAL-INJECTION-01.
//
// The check scans all parameters in the FuncDecl's parameter list (including
// multi-parameter functions). A parameter is considered clock.Clock-typed if
// its go/types resolved type is the named type in kernelClockPkgPath.
//
// Blind spot: if info is nil (untyped scan), returns false conservatively.
func clockOptionInjectorHasClockParam(fd *ast.FuncDecl, info *types.Info) bool {
	if info == nil || fd.Type == nil || fd.Type.Params == nil {
		return false
	}
	for _, field := range fd.Type.Params.List {
		for _, nameIdent := range field.Names {
			obj, ok := info.ObjectOf(nameIdent).(*types.Var)
			if !ok {
				continue
			}
			if isKernelClockType(obj.Type()) {
				return true
			}
		}
		// Handle anonymous parameters (no name) — check the field type directly.
		if len(field.Names) == 0 {
			if ident, ok := typeExprToIdent(field.Type); ok {
				if obj, ok2 := info.ObjectOf(ident).(*types.TypeName); ok2 {
					if isKernelClockType(obj.Type()) {
						return true
					}
				}
			}
		}
	}
	return false
}

// typeExprToIdent extracts the terminal *ast.Ident from a potentially qualified
// type expression (SelectorExpr or Ident). Returns (nil, false) for other forms.
func typeExprToIdent(expr ast.Expr) (*ast.Ident, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		return e, true
	case *ast.SelectorExpr:
		return e.Sel, true
	}
	return nil, false
}

// isKernelClockType reports whether t is the kernel/clock.Clock named interface type.
func isKernelClockType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == kernelClockPkgPath && obj.Name() == "Clock"
}

// clockOptionInjectorReturnsOption reports whether fd's return type is a
// function/option type — i.e. the functional-option shape. This is predicate (3)
// of sub-check B in CLOCK-POSITIONAL-INJECTION-01.
//
// A return type qualifies if its underlying type (after resolving named types)
// is a *types.Signature (function type). This covers both direct `func(*T)`
// returns and named types like `type Option func(*T)` and `type EnvOption func(*cfg)`.
//
// Multi-return functions: checks only the first return value — a function
// returning (Option, error) still makes clock injection omittable.
func clockOptionInjectorReturnsOption(fd *ast.FuncDecl, info *types.Info) bool {
	if info == nil || fd.Type == nil || fd.Type.Results == nil {
		return false
	}
	// Collect all result type expressions from the AST.
	results := fd.Type.Results.List
	if len(results) == 0 {
		return false
	}
	// Check the first return type.
	firstField := results[0]
	return isOptionTypeExpr(firstField.Type, info)
}

// isOptionTypeExpr reports whether the AST type expression resolves to a
// function type (or named type with function underlying type) via go/types.
func isOptionTypeExpr(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok {
		return false
	}
	t := tv.Type
	if t == nil {
		return false
	}
	return isUnderlyingFunc(t)
}

// isUnderlyingFunc reports whether t's underlying type is a *types.Signature.
func isUnderlyingFunc(t types.Type) bool {
	if t == nil {
		return false
	}
	_, isSig := t.Underlying().(*types.Signature)
	return isSig
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

	// GREEN: compliant (arg0 is a param; no clock option-injector).
	// RED: violations (selector arg0; exported With*Clock option-injector).
	dirs := []string{
		"param_passes",                     // GREEN: MustHaveClock(clk, ...) where clk is a param
		"selector_violates",                // RED: MustHaveClock(cfg.Clock, ...) — selector arg0
		"withclock_violates",               // RED: exported func WithClock(...) — exact name
		"withfooclock_violates",            // RED: exported func WithFooClock(...) — suffixed name (broadened predicate)
		"aliased_import_selector_violates", // RED: aliased import + selector arg0
		"ctx_param_passes",                 // GREEN: (ctx, clk) two-param form — clk is a param
		"struct_field_violates",            // RED: exported Config-suffix struct with exported Clock field (sub-check C)
		"struct_field_unexported_ok",       // GREEN: unexported struct + unexported field; non-Config suffix
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
