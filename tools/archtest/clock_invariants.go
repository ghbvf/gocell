// clock_invariants.go — importable clock injection rule logic (#1640 M3 PR-9).
//
// Non-test home for KERNEL-CLOCK-LEAF-FALLBACK-01,
// KERNEL-CLOCK-RESET-RELATIVE-PROD-01, PROD-CLOCK-INJECTION-01, and
// CLOCK-POSITIONAL-INJECTION-01 scanner logic, so it can be compiled and run
// by an external Cell repository (Go never compiles a dependency's _test.go,
// so rule logic external repos must run cannot live in a _test.go file).
// GoCell's own Test* functions in clock_invariants_test.go dogfood the same
// Check* — single source, no parallel rule body.
//
// Four rules are implemented here:
//
//   - KERNEL-CLOCK-LEAF-FALLBACK-01: leaf-level clock.Real() construction is
//     forbidden outside the composition root.
//   - KERNEL-CLOCK-RESET-RELATIVE-PROD-01: production code must use the absolute
//     Timer.ResetAt(deadline time.Time) API.
//   - PROD-CLOCK-INJECTION-01: no direct reference to stdlib time wall-clock entry
//     points in production code; all wall-clock interactions must flow through
//     an injected kernel/clock.Clock.
//   - CLOCK-POSITIONAL-INJECTION-01: clock.MustHaveClock arg0 must be a positional
//     parameter; no exported clock option-injector functions; no exported
//     Config/Options struct fields of type clock.Clock.
//
// Dogfood tests: TestKernelClockLeafFallback, TestKernelClockResetRelativeProd,
// TestProdClockInjection, TestClockPositionalInjection (clock_invariants_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: allowlist/scan-scope is gocell-hardcoded
// (kernel/clock layout, composition-root carve-outs) with no ConfigForExternalCell
// consumer-extension -> vacuous/false-red externally; kept importable +
// module-path-agnostic + fork-safe.
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

// ---------------------------------------------------------------------------
// Shared package-path constants (module-path-agnostic)
// ---------------------------------------------------------------------------

// clockKernelClockPkgPath is the import path of kernel/clock.
// Derived from PlatformModulePath so a module rename / /v2 bump updates exactly
// one place.
const clockKernelClockPkgPath = PlatformModulePath + "/kernel/clock"

// clockMustHaveClockFuncName is the function name in clockKernelClockPkgPath
// that enforces the positional injection contract.
const clockMustHaveClockFuncName = "MustHaveClock"

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-LEAF-FALLBACK-01
// ---------------------------------------------------------------------------
//
// Leaf-level clock.Real() construction is forbidden outside the composition
// root. All production code must accept clock.Clock as a constructor parameter
// and validate it via clock.MustHaveClock.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
//
// # AI-robust grade
//
// Downstream Hard: this archtest locks the form (clock.Real() callsite identity
// via go/types; import aliases cannot bypass). Upstream Medium: Go has no
// friend-package mechanism, so the compiler cannot prevent a new clock.Real()
// call from being added inside the kernel/clock package itself; archtest catches
// it in CI. Permanent ceiling same as SPAN-SETATTR-HOLDER-SEAL (gh #851).
//
// # Not registered in StandardCellRules
//
// Allowlist (allowedRealCallerPaths) is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension; the composition-root paths listed
// are GoCell-specific -> would be vacuous-green (no matching files) in an
// external repo.

// clockAllowedRealCallerPaths lists the production-code paths that may call
// kernel/clock.Real() directly.
var clockAllowedRealCallerPaths = []string{
	"kernel/clock/clock.go",                   // Real() factory definition
	"cmd/corebundle/",                         // main composition root
	"cmd/gocell/",                             // gocell CLI composition root
	"gocell.go",                               // top-level entry
	"tests/e2e/internal/clients/clients.go",   // e2e suite composition root
	"corecells/accesscore/internal/testutil/", // SessionRepoForTest / RealSessionRepo
	"corecells/configcore/configcoretest/",    // BuildWriteService / BuildSubscribeService default clock
}

// clockIsAllowedRealCallerPath reports whether rel is exempt from the gate.
func clockIsAllowedRealCallerPath(rel string) bool {
	for _, allowed := range clockAllowedRealCallerPaths {
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

// clockScanLeafRealCallsAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every call to kernel/clock.Real().
func clockScanLeafRealCallsAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
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
		if clockMatchedKernelClockReal(info, e.Sel) {
			record(e)
		}
	})
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		if clockMatchedKernelClockReal(info, e) {
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

// clockMatchedKernelClockReal reports whether ident resolves to kernel/clock.Real.
func clockMatchedKernelClockReal(info *types.Info, ident *ast.Ident) bool {
	if info == nil || ident == nil {
		return false
	}
	fn, ok := info.ObjectOf(ident).(*types.Func)
	if !ok {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != clockKernelClockPkgPath {
		return false
	}
	if sig, _ := fn.Type().(*types.Signature); sig != nil && sig.Recv() != nil {
		return false
	}
	return fn.Name() == "Real"
}

// CheckKernelClockLeafFallback runs KERNEL-CLOCK-LEAF-FALLBACK-01 against the
// go.work workspace production scope and returns its diagnostics. Does not call t.Errorf —
// callers funnel diagnostics through Report.
//
// Not registered in StandardCellRules: see file godoc.
func CheckKernelClockLeafFallback(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()

	return Run(t, Production(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if clockIsAllowedRealCallerPath(rel) {
					continue
				}
				d = append(d, clockScanLeafRealCallsAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// ---------------------------------------------------------------------------
// KERNEL-CLOCK-RESET-RELATIVE-PROD-01
// ---------------------------------------------------------------------------
//
// Production code must use the absolute Timer.ResetAt(deadline time.Time) API
// instead of the relative Timer.Reset(d time.Duration) to avoid the
// read-then-act race on timer reset.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
//
// # AI-robust grade
//
// Downstream Hard: this archtest locks the form (type-driven signature check on
// Reset receiver; import aliases cannot bypass go/types resolution). Upstream
// Medium: stdlib time.Timer.Reset is a free-standing method and cannot be made
// uncallable in Go (same permanent ceiling as SPAN-SETATTR-REDACT-01
// package-internal axis).
//
// # Not registered in StandardCellRules
//
// Scan scope is the go.work workspace production scope; there is no
// ConfigForExternalCell consumer-extension for the exempt paths
// (clockResetRelativeExemptPaths) -> would be vacuous-green in an external repo.

// clockResetRelativeExemptPaths lists path prefixes exempt from the gate.
var clockResetRelativeExemptPaths = []string{
	"kernel/clock/clock.go",   // interface definition
	"kernel/clock/clockmock/", // fake implementation
}

// clockIsResetRelativeExempt reports whether rel is exempt from the gate.
func clockIsResetRelativeExempt(rel string) bool {
	for _, prefix := range clockResetRelativeExemptPaths {
		if strings.HasPrefix(rel, prefix) || rel == prefix {
			return true
		}
	}
	return false
}

// clockCheckResetCall inspects a single CallExpr and, if it is a call to
// Reset(time.Duration) on a type that also exposes ResetAt, appends a
// Diagnostic to *out (deduped by seen). Extracted from clockScanResetRelativeAST
// to reduce cognitive complexity of the walk body.
func clockCheckResetCall(fset *token.FileSet, call *ast.CallExpr, rel string, info *types.Info, seen map[string]bool, out *[]Diagnostic) {
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
	if !clockIsResetDurationBool(sig) {
		return
	}
	recvType := sig.Recv().Type()
	if !clockTypeHasResetAt(recvType) {
		return
	}
	line := fset.Position(call.Pos()).Line
	key := fmt.Sprintf("%s:%d", rel, line)
	if !seen[key] {
		seen[key] = true
		*out = append(*out, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: "Timer.Reset(d time.Duration) — use ResetAt(deadline time.Time) instead " +
				"to avoid read-then-act race; " +
				"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md",
		})
	}
}

// clockScanResetRelativeAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every call `<expr>.Reset(d)` on a type that also
// exposes ResetAt.
func clockScanResetRelativeAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		clockCheckResetCall(fset, call, rel, info, seen, &out)
	})

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// clockIsResetDurationBool reports whether sig matches Reset(time.Duration) bool.
func clockIsResetDurationBool(sig *types.Signature) bool {
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

// clockTypeHasResetAt reports whether t exposes a method ResetAt(time.Time) bool.
func clockTypeHasResetAt(t types.Type) bool {
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
	return clockIsTimeTimeType(param)
}

// clockIsTimeTimeType reports whether t is time.Time.
func clockIsTimeTimeType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == "time" && obj.Name() == "Time"
}

// CheckKernelClockResetRelativeProd runs KERNEL-CLOCK-RESET-RELATIVE-PROD-01
// against the go.work workspace production scope and returns its diagnostics. Does not call
// t.Errorf — callers funnel diagnostics through Report.
//
// Not registered in StandardCellRules: see file godoc.
func CheckKernelClockResetRelativeProd(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	return Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if clockIsResetRelativeExempt(rel) {
					continue
				}
				d = append(d, clockScanResetRelativeAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// ---------------------------------------------------------------------------
// PROD-CLOCK-INJECTION-01
// ---------------------------------------------------------------------------
//
// No direct reference to stdlib time wall-clock entry points (time.Now,
// time.Since, time.Until, time.NewTimer, etc.) in production code. All
// wall-clock interactions must flow through an injected kernel/clock.Clock.
//
// Resolution is type-driven: every *ast.SelectorExpr and bare *ast.Ident is
// run through go/types.Info.ObjectOf to obtain the resolved *types.Func, then
// gated on obj.Pkg().Path() == "time" and obj.Name() in forbiddenTimeFns.
// This makes the check immune to import aliases and dot-imports.
//
// Control-plane carve-out (host-scoped (host, method, callee) triple, #619 +
// #1275 F1): a FuncDecl is exempt from PROD-CLOCK-INJECTION-01 only if it is a
// METHOD whose receiver type name is "controlPlaneClock", whose file is under a
// sanctioned host (clockControlPlaneCarveOut keys: "kernel/reconcile/"), AND
// whose name maps to the exact stdlib callee listed for THAT host. This replaces
// the former comment-marker + hand-maintained allowlist-map form (which was
// AI-abusable: any marked FuncDecl in any allowlisted file could self-exempt by
// adding the comment).
//
// AI-robust grade: Medium. Permanent ceiling: stdlib time.NewTicker/time.NewTimer
// free functions cannot be made uncallable in Go (same as SPAN-SETATTR-REDACT-01
// package-internal axis). Gain over prior form: eliminated AI-abusable comment
// marker and hand-maintained allowlist map (see clockControlPlaneAllowedMethods).
//
// ref: docs/architecture/202605170000-adr-control-plane-business-plane-decouple.md §D-A
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
// ref: dominikh/go-tools analysis/code/code.go CallName / IsCallToAny
//
// # Not registered in StandardCellRules
//
// Scan scope is the go.work workspace production scope plus composition-root
// carve-outs (clockControlPlaneCarveOut) that are gocell-hardcoded with no
// ConfigForExternalCell consumer-extension -> vacuous-green in an external repo.

// clockAllowedRealClockPaths lists the paths whose files may legitimately
// reference stdlib time symbols directly.
//
// Entry semantics: a trailing "/" marks a directory prefix; otherwise exact.
var clockAllowedRealClockPaths = []string{
	"kernel/clock/",
	"pkg/testutil/testwait/testwait.go",
}

// clockForbiddenTimeFns maps each forbidden stdlib time function to the
// equivalent Clock interface method that production callers must use instead.
var clockForbiddenTimeFns = map[string]string{
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

// clockControlPlaneCarveOut maps each sanctioned control-plane host package
// (directory prefix, trailing "/") to the exact set of (controlPlaneClock method
// name -> stdlib time.* function name, without the "time." prefix) pairs that
// host's methods may call directly. The carve-out key is the (host, method,
// callee) TRIPLE: a method is exempt ONLY in the host that declares it.
//
// Why host-scoped, not a global method->callee map (#1275 review F1): a flat,
// host-agnostic map let any host borrow any other host's method exception.
// Binding the method set to its host closes both: a different host's methods are
// not exempt in kernel/reconcile, and adding a second host grants NOTHING until
// that host gets its own explicit (method->callee) entries here.
//
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md §#619
var clockControlPlaneCarveOut = map[string]map[string]string{
	"kernel/reconcile/": {
		"newProbeTimer":   "NewTimer",  // startup probe
		"newRequeueTimer": "NewTimer",  // delayed requeue (RECONCILE-LOOP-CLOCK-CARVEOUT-01)
		"newRenewTicker":  "NewTicker", // leader lease renew cadence (PR-A6 RECONCILE-LEADER)
		"now":             "Now",       // reconcile-duration measurement
	},
}

// clockControlPlaneHostMethods returns the (method name -> sanctioned callee) set
// for the control-plane host package that rel belongs to, or nil if rel is under
// no sanctioned host (gate (a)).
func clockControlPlaneHostMethods(rel string) map[string]string {
	for host, methods := range clockControlPlaneCarveOut {
		if strings.HasPrefix(rel, host) {
			return methods
		}
	}
	return nil
}

// clockAllowedMethods returns the map of FuncDecl name-positions
// (format: fset.Position(fd.Name.Pos()).String()) to the sanctioned stdlib
// callee for methods in file that are candidates for the PROD-CLOCK-INJECTION-01
// carve-out.
//
// A FuncDecl is a candidate if and only if ALL of:
//
//	(a) rel is under a sanctioned control-plane host package.
//	(b) fd.Recv != nil — it is a method, not a free function.
//	(c) The receiver's type name is "controlPlaneClock".
//	(d) The method name is listed FOR THIS HOST in clockControlPlaneCarveOut.
//
// AI-robust grade: Medium (permanent ceiling).
//
// Linear multi-gate AST walk; each gate (a)-(d) is a necessary predicate;
// splitting into smaller helpers adds indirection without reducing the
// conceptual complexity of the (host, method, callee) triple check.
func clockAllowedMethods(fset *token.FileSet, file *ast.File, rel string) map[string]string {
	out := map[string]string{}
	// Gate (a): resolve the host's (method->callee) carve-out set.
	hostMethods := clockControlPlaneHostMethods(rel)
	if hostMethods == nil {
		return out
	}
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		// Gate (b): must be a method (has receiver list).
		if fd.Recv == nil || len(fd.Recv.List) == 0 {
			return
		}
		// Gate (c): receiver type name must be "controlPlaneClock".
		if clockReceiverTypeName(fd) != "controlPlaneClock" {
			return
		}
		if fd.Name == nil {
			return
		}
		// Gate (d): the method must be listed FOR THIS HOST.
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
// helper exported by the archtest package (walk.go).
func clockReceiverTypeName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return ""
	}
	return ReceiverTypeName(fd.Recv.List[0].Type)
}

// findTopLevelFuncDecl returns the top-level *ast.FuncDecl named funcName
// and true if found; otherwise nil and false.
// Named without a "clock" prefix so that sibling test files in package archtest
// (span_setattr_redact_test.go, http_idempotency_assembly_scope_test.go) can
// continue to call it directly — they have been using this helper before it
// moved to a non-test file.
func findTopLevelFuncDecl(file *ast.File, funcName string) (*ast.FuncDecl, bool) {
	var result *ast.FuncDecl
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name != nil && fd.Name.Name == funcName {
			result = fd
		}
	})
	return result, result != nil
}

// clockEnclosingFuncDeclKey returns the position-string key for the nearest
// enclosing top-level *ast.FuncDecl that directly (not via a FuncLit/closure)
// contains pos. Returns "" if pos is not inside any FuncDecl body, or if pos
// is inside a nested FuncLit within a FuncDecl body.
//
// The key matches the format produced by clockAllowedMethods.
func clockEnclosingFuncDeclKey(fset *token.FileSet, file *ast.File, pos token.Pos) string {
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

// clockIsAllowedRealClockPath reports whether rel is covered by one of the
// clockAllowedRealClockPaths entries.
func clockIsAllowedRealClockPath(rel string) bool {
	for _, p := range clockAllowedRealClockPaths {
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

// clockResolvedFunc returns the *types.Func for a call expression's function
// expression, or nil if it cannot be determined.
func clockResolvedFunc(fun ast.Expr, info *types.Info) *types.Func {
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

// clockMatchedTimeFn reports whether ident resolves to a forbidden stdlib time function.
func clockMatchedTimeFn(info *types.Info, ident *ast.Ident) (string, bool) {
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
	if _, listed := clockForbiddenTimeFns[name]; !listed {
		return "", false
	}
	return name, true
}

// scanProdClockInjectionAST walks file's AST and returns a sorted slice of
// violation Diagnostics for every reference to one of the forbidden stdlib
// time functions.
//
// Control-plane carve-out: a forbidden reference is exempt only if it sits in a
// top-level *ast.FuncDecl that is a METHOD on receiver type "controlPlaneClock",
// in a file under a sanctioned host (clockControlPlaneCarveOut keys:
// "kernel/reconcile/"), AND the method/callee match the exact (host, method,
// callee) triple listed for that host.
//
// Linear multi-branch scanner; each branch handles one of the two AST forms
// (SelectorExpr / bare Ident); the carve-out record() closure is the common
// path; no structural reduction is possible without hiding the (host, method,
// callee) triple logic.
//
//nolint:gocognit // R2-approved: linear two-form (Selector/Ident) scanner; shared carve-out closure; additive per form, not nested.
func scanProdClockInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	allowedFuncs := clockAllowedMethods(fset, file, rel)

	record := func(node ast.Node, name string) {
		if funcKey := clockEnclosingFuncDeclKey(fset, file, node.Pos()); funcKey != "" {
			if sanctionedCallee, isCandidate := allowedFuncs[funcKey]; isCandidate {
				if sanctionedCallee == name {
					return // sanctioned (host, method, callee) triple
				}
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
				name, clockForbiddenTimeFns[name],
			),
		})
	}

	EachInSubtree[ast.SelectorExpr](file, func(e *ast.SelectorExpr) {
		if name, ok := clockMatchedTimeFn(info, e.Sel); ok {
			record(e, name)
		}
	})
	EachInSubtree[ast.Ident](file, func(e *ast.Ident) {
		if name, ok := clockMatchedTimeFn(info, e); ok {
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

// CheckProdClockInjection runs PROD-CLOCK-INJECTION-01 against the go.work
// workspace production scope and returns its diagnostics. Does not call t.Errorf
// — callers funnel diagnostics through Report.
//
// Not registered in StandardCellRules: see file godoc.
func CheckProdClockInjection(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	// Intentionally not honoring testing.Short — see original TestProdClockInjection.

	return Run(t, Production(TypedOpts{Tests: true, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			var d []Diagnostic
			for _, f := range p.Files {
				rel := p.Rel(f)
				if !fileroles.IsProductionCode(rel) {
					continue
				}
				if clockIsAllowedRealClockPath(rel) {
					continue
				}
				d = append(d, scanProdClockInjectionAST(p.Fset, f, rel, p.TypesInfo)...)
			}
			return d
		})
}

// ---------------------------------------------------------------------------
// CLOCK-POSITIONAL-INJECTION-01
// ---------------------------------------------------------------------------
//
// Two sub-checks over production code (excludes _test.go, generated/,
// testdata/, and recognized test-helper packages):
//
// Sub-check A: MustHaveClock positional-param enforcement.
// Every production call to clock.MustHaveClock(arg0, ...) must have arg0
// resolve (via go/types) to a PARAMETER of the enclosing function.
//
// Sub-check B: clock option-injector ban (broadened).
// Any exported free function (no receiver) in a production package is a
// violation if it: (1) name contains "Clock", (2) parameter list includes a
// kernel/clock.Clock-typed parameter, (3) return type is a function/option type.
//
// Sub-check C: input Config/Options struct field ban (#1136 review F1).
// Any EXPORTED type declaration whose name has the suffix "Config"/"Options"/
// "Opts" AND which has an EXPORTED struct field whose type resolves to
// kernel/clock.Clock is a violation.
//
// AI-robust grading:
//
//   - Downstream Hard: this archtest locks the form (any MustHaveClock call
//     with a non-param arg0 is a violation; any exported clock-option-injector
//     function is a violation).
//
//   - Upstream Hard: the Go compiler is the upstream enforcement. A mandatory
//     positional `clk clock.Clock` parameter makes omission a compile error.
//
// ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D
//
// # Not registered in StandardCellRules
//
// Scan scope is the go.work workspace production scope; carve-outs are
// gocell-hardcoded (composition-root naming convention) with no ConfigForExternalCell
// consumer-extension -> vacuous-green in an external repo.

// clockIsMustHaveClockCall reports whether call is a call to
// kernel/clock.MustHaveClock. Resolution is type-driven via go/types.Info so
// import aliases do not bypass.
func clockIsMustHaveClockCall(call *ast.CallExpr, info *types.Info) bool {
	if info == nil {
		return false
	}
	fn := clockResolvedFunc(call.Fun, info)
	if fn == nil {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != clockKernelClockPkgPath {
		return false
	}
	return fn.Name() == clockMustHaveClockFuncName
}

// clockIsKernelClockType reports whether t is the kernel/clock.Clock named interface type.
func clockIsKernelClockType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Pkg() != nil && obj.Pkg().Path() == clockKernelClockPkgPath && obj.Name() == "Clock"
}

// clockTypeExprToIdent extracts the terminal *ast.Ident from a potentially
// qualified type expression (SelectorExpr or Ident). Returns (nil, false) for
// other forms.
func clockTypeExprToIdent(expr ast.Expr) (*ast.Ident, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		return e, true
	case *ast.SelectorExpr:
		return e.Sel, true
	}
	return nil, false
}

// clockArg0IsParam reports whether arg0 (the first argument to MustHaveClock)
// is a direct reference to a parameter of the enclosing top-level FuncDecl.
func clockArg0IsParam(file *ast.File, arg0 ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	ident, ok := arg0.(*ast.Ident)
	if !ok {
		return false
	}
	obj, ok := info.ObjectOf(ident).(*types.Var)
	if !ok {
		return false
	}
	fd, found := FindFirstChild[ast.FuncDecl](file, func(fd *ast.FuncDecl) bool {
		return fd.Body != nil && fd.Body.Pos() <= arg0.Pos() && arg0.End() <= fd.Body.End()
	})
	if !found {
		return false
	}
	if fd.Type == nil || fd.Type.Params == nil {
		return false
	}
	return clockFuncDeclHasParam(fd, obj, info)
}

// clockFuncDeclHasParam reports whether obj is one of the parameters declared in
// fd's parameter list.
func clockFuncDeclHasParam(fd *ast.FuncDecl, obj *types.Var, info *types.Info) bool {
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
			if paramVar.Pos() == obj.Pos() {
				return true
			}
		}
	}
	return false
}

// clockAnonFieldIsKernelClock reports whether an anonymous (unnamed) field's
// type resolves to kernel/clock.Clock via go/types.
func clockAnonFieldIsKernelClock(field *ast.Field, info *types.Info) bool {
	ident, ok := clockTypeExprToIdent(field.Type)
	if !ok {
		return false
	}
	obj, ok2 := info.ObjectOf(ident).(*types.TypeName)
	if !ok2 {
		return false
	}
	return clockIsKernelClockType(obj.Type())
}

// clockOptionInjectorHasClockParam reports whether fd's parameter list includes
// a parameter of type kernel/clock.Clock.
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
			if clockIsKernelClockType(obj.Type()) {
				return true
			}
		}
		// Handle anonymous parameters (no name).
		if len(field.Names) == 0 && clockAnonFieldIsKernelClock(field, info) {
			return true
		}
	}
	return false
}

// clockIsOptionTypeExpr reports whether the AST type expression resolves to a
// function type (or named type with function underlying type) via go/types.
func clockIsOptionTypeExpr(expr ast.Expr, info *types.Info) bool {
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
	_, isSig := t.Underlying().(*types.Signature)
	return isSig
}

// clockOptionInjectorReturnsOption reports whether fd's return type is a
// function/option type.
func clockOptionInjectorReturnsOption(fd *ast.FuncDecl, info *types.Info) bool {
	if info == nil || fd.Type == nil || fd.Type.Results == nil {
		return false
	}
	results := fd.Type.Results.List
	if len(results) == 0 {
		return false
	}
	return clockIsOptionTypeExpr(results[0].Type, info)
}

// clockIsInputConfigStructName reports whether name matches the framework's input
// Config/Options struct naming convention used by sub-check C.
func clockIsInputConfigStructName(name string) bool {
	return strings.HasSuffix(name, "Config") ||
		strings.HasSuffix(name, "Options") ||
		strings.HasSuffix(name, "Opts")
}

// clockStructFieldIsExported reports whether field has at least one exported name,
// or — for anonymous (embedded) fields — whether the embedded type's terminal
// identifier is exported.
func clockStructFieldIsExported(field *ast.Field) bool {
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
	ident, ok := clockTypeExprToIdent(field.Type)
	if !ok {
		return false
	}
	return ident.IsExported()
}

// clockStructFieldIsKernelClock reports whether field declares at least one
// kernel/clock.Clock-typed identifier (resolved via go/types).
func clockStructFieldIsKernelClock(field *ast.Field, info *types.Info) bool {
	if info == nil || field == nil {
		return false
	}
	if len(field.Names) > 0 {
		obj, ok := info.ObjectOf(field.Names[0]).(*types.Var)
		if !ok {
			return false
		}
		return clockIsKernelClockType(obj.Type())
	}
	ident, ok := clockTypeExprToIdent(field.Type)
	if !ok {
		return false
	}
	obj, ok := info.ObjectOf(ident).(*types.TypeName)
	if !ok {
		return false
	}
	return clockIsKernelClockType(obj.Type())
}

// clockSubCheckA appends sub-check-A violations (MustHaveClock arg0 must be a
// positional parameter) to out, deduped via seen.
func clockSubCheckA(fset *token.FileSet, file *ast.File, rel string, info *types.Info, seen map[string]bool, out *[]Diagnostic) {
	EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		if !clockIsMustHaveClockCall(call, info) {
			return
		}
		if len(call.Args) == 0 {
			return
		}
		arg0 := call.Args[0]
		if clockArg0IsParam(file, arg0, info) {
			return
		}
		line := fset.Position(call.Pos()).Line
		key := fmt.Sprintf("%s:%d:A", rel, line)
		if !seen[key] {
			seen[key] = true
			*out = append(*out, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "clock.MustHaveClock: arg0 must be a function parameter (positional injection); " +
					"selector or non-ident arg0 (e.g. cfg.Clock, s.clk) violates the positional-param contract. " +
					"ref: docs/architecture/202605270000-adr-clock-positional-injection-funnel.md",
			})
		}
	})
}

// clockSubCheckB appends sub-check-B violations (ban exported clock
// option-injector free functions) to out, deduped via seen.
func clockSubCheckB(fset *token.FileSet, file *ast.File, rel string, info *types.Info, seen map[string]bool, out *[]Diagnostic) {
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil {
			return
		}
		if !strings.Contains(fd.Name.Name, "Clock") {
			return
		}
		if !fd.Name.IsExported() {
			return
		}
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			return
		}
		if !clockOptionInjectorHasClockParam(fd, info) {
			return
		}
		if !clockOptionInjectorReturnsOption(fd, info) {
			return
		}
		line := fset.Position(fd.Name.Pos()).Line
		key := fmt.Sprintf("%s:%d:B", rel, line)
		if !seen[key] {
			seen[key] = true
			*out = append(*out, Diagnostic{
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
}

// clockCheckConfigStructField appends a sub-check-C violation for a single
// struct field if it is an exported kernel/clock.Clock field.
func clockCheckConfigStructField(
	fset *token.FileSet, ts *ast.TypeSpec, field *ast.Field,
	rel string, info *types.Info, seen map[string]bool, out *[]Diagnostic,
) {
	if !clockStructFieldIsKernelClock(field, info) {
		return
	}
	if !clockStructFieldIsExported(field) {
		return
	}
	reportPos := field.Type.Pos()
	fieldName := "<anonymous>"
	if len(field.Names) > 0 {
		reportPos = field.Names[0].Pos()
		fieldName = field.Names[0].Name
	}
	line := fset.Position(reportPos).Line
	key := fmt.Sprintf("%s:%d:C", rel, line)
	if seen[key] {
		return
	}
	seen[key] = true
	*out = append(*out, Diagnostic{
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

// clockSubCheckC appends sub-check-C violations (input Config/Options struct
// exported clock.Clock field ban) to out, deduped via seen.
func clockSubCheckC(fset *token.FileSet, file *ast.File, rel string, info *types.Info, seen map[string]bool, out *[]Diagnostic) {
	EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if ts.Name == nil || !ts.Name.IsExported() || !clockIsInputConfigStructName(ts.Name.Name) {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			clockCheckConfigStructField(fset, ts, field, rel, info, seen, out)
		}
	})
}

// scanClockPositionalInjectionAST runs all three sub-checks (A, B, C) over a
// single file and returns all violations, sorted by line. The three sub-checks
// share the dedup map so callers see a single sorted slice.
func scanClockPositionalInjectionAST(fset *token.FileSet, file *ast.File, rel string, info *types.Info) []Diagnostic {
	var out []Diagnostic
	seen := map[string]bool{}

	// Sub-check A: MustHaveClock arg0 must be a parameter.
	clockSubCheckA(fset, file, rel, info, seen, &out)
	// Sub-check B: ban exported clock option-injector free functions.
	clockSubCheckB(fset, file, rel, info, seen, &out)
	// Sub-check C: input Config/Options struct field ban.
	clockSubCheckC(fset, file, rel, info, seen, &out)

	sort.Slice(out, func(i, j int) bool {
		if out[i].Rel != out[j].Rel {
			return out[i].Rel < out[j].Rel
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// CheckClockPositionalInjection runs CLOCK-POSITIONAL-INJECTION-01 against the
// go.work workspace production scope and returns its diagnostics. Does not call t.Errorf — callers
// funnel diagnostics through Report.
//
// Not registered in StandardCellRules: see file godoc.
func CheckClockPositionalInjection(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	return Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
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
}
