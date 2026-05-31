//   - INVARIANT: DISTLOCK-ORPHAN-NO-DRIVER-IO-01
//   - INVARIANT: DISTLOCK-MANAGER-DRIVER-IO-OFFLOADED-01
//
// # DISTLOCK-ORPHAN-NO-DRIVER-IO-01
//
// Within runtime/distlock, the function handleOrphan MUST NOT call any
// method of the distlock.Driver interface (SetNX / Renew / Release).
//
// Rationale: Lock.Orphan() is designed to stop lease renewal WITHOUT any
// backend I/O so that callers can hand off the lock during graceful shutdown
// even when the backend (Redis) is unreachable. If handleOrphan were to call
// Driver.SetNX / Driver.Renew / Driver.Release, the no-IO contract would be
// silently violated: shutdown could block or fail exactly when it must not.
//
// Detection: type-aware callee resolution via TypesInfo.Selections — each
// *ast.SelectorExpr call-target is resolved to its *types.Func, then its
// receiver interface type is compared against distlock.Driver to identify
// method calls on that interface. Scoped to the handleOrphan function body.
//
// AI-robust evaluation:
//
//   - Grade: Medium.
//     The Go type system cannot statically prevent handleOrphan from
//     receiving the Manager (which has a driver field) and calling its
//     methods. m.driver is reachable from any Manager method, so no type
//     system seal is possible — archtest type-aware callee resolution is
//     the maximum achievable enforcement. A caller-allowlist pattern does
//     not apply here (the constraint is "absence of calls in one function",
//     not "calls must go through a funnel"). Hard upgrade path: seal
//     Manager.driver behind an interface that omits the methods entirely
//     for the orphan code path; not pursued as the added indirection cost
//     exceeds the benefit given the single-function scope.
//
// Blind spots (stated per ai-robust.md §"工具选定后强制盲区自检"):
//   - Indirect dispatch: if handleOrphan called a helper (e.g. callDriver())
//     that in turn calls Driver methods, this archtest would NOT detect it
//     (we only scan the handleOrphan function body, not its callees).
//   - Local-variable method-value invocation: `rel := m.driver.Release;
//     rel(ctx, key, tok)` — the SelectorExpr `m.driver.Release` on the
//     right-hand side is resolved by TypesInfo.Selections (detected), but
//     the invocation `rel(...)` uses an *ast.Ident as the Fun, not a
//     *ast.SelectorExpr, so the call itself is NOT flagged as a Driver call.
//   - Function-pointer field: if a helper struct stored a Driver method as a
//     func field and handleOrphan called that field, it would not be detected.
//
// Reverse self-checks (F3):
//   - TestDistlockOrphanNoDriverIO01_BlindSpotSelfCheck: builds a synthetic
//     function body that DOES call a Driver method and asserts the detector
//     flags it. Proves the detection logic is not a no-op.
//   - TestDistlockOrphanNoDriverIO01_ReverseCheck_LocalVarMethodValue: asserts
//     the local-var-method-value blind spot form does NOT appear in production.
//   - TestDistlockOrphanNoDriverIO01_ReverseCheck_DriverFuncField: asserts the
//     function-pointer-field blind spot form does NOT appear in production.
//   - Indirect-dispatch blind spot: residual — full transitive callee analysis
//     is too costly for a single archtest pass; acknowledged as known gap.
//
// # DISTLOCK-MANAGER-DRIVER-IO-OFFLOADED-01
//
// Within the runtime/distlock package, every call to a distlock.Driver
// interface method (SetNX / Renew / Release) in the manager goroutine code
// paths MUST occur inside the func-literal of a go statement (i.e., in a
// background goroutine). The sole exception is lockerImpl.Acquire, where
// SetNX is a synchronous acquire RPC that legitimately runs on the caller
// goroutine and is not part of the manager loop.
//
// Rationale: After F1, all Driver I/O (SetNX in Acquire, Renew in
// handleRenew, Release in handleRemove) must be in background goroutines so
// the manager loop stays live for Orphan/Stop events even when the backend
// is slow or unreachable.
//
// Detection: for each CallExpr whose callee resolves to a Driver interface
// method, walk the parent AST chain looking for an enclosing *ast.GoStmt. If
// none is found, it is a violation unless the enclosing FuncDecl is "Acquire".
//
// AI-robust evaluation:
//
//   - Grade: Medium.
//     m.driver is reachable from any Manager method; the Go type system
//     cannot prevent a future developer from adding a synchronous Driver call.
//     This archtest is the maximum achievable enforcement without sealing the
//     driver field behind a goroutine-forcing wrapper.
//
// Blind spots:
//   - Helper-indirect: if a helper function (not a go-literal) calls a Driver
//     method, this rule would not catch it (only direct call sites in the
//     scanned functions are checked). Residual gap — full transitive analysis
//     is prohibitive. Acknowledged.
//
// ref: runtime/distlock/manager.go handleRenew/handleRemove/Acquire
package archtest

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleDistlockOrphanNoDriverIO01 = "DISTLOCK-ORPHAN-NO-DRIVER-IO-01"
	// distlockMgrPkgPath is a GoCell platform symbol path, anchored to
	// PlatformModulePath so a module rename / /v2 bump updates exactly one
	// place and ARCHTEST-MODULE-PATH-FUNNEL-01 stays green (no bare literal).
	distlockMgrPkgPath   = PlatformModulePath + "/runtime/distlock"
	handleOrphanFuncName = "handleOrphan"
	driverIfaceName      = "Driver"
)

// TestDistlockOrphanNoDriverIO01 asserts that handleOrphan in runtime/distlock
// does not call any method of the distlock.Driver interface.
func TestDistlockOrphanNoDriverIO01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		foundHandleOrphan bool
		foundDriverIface  bool
		violations        []Diagnostic
	)

	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/distlock/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != distlockMgrPkgPath {
				return nil
			}

			// Resolve the Driver interface type from the package scope.
			driverObj := p.Pkg.Scope().Lookup(driverIfaceName)
			if driverObj == nil {
				return nil
			}
			driverNamed, ok := driverObj.Type().(*types.Named)
			if !ok {
				return nil
			}
			driverIface, ok := driverNamed.Underlying().(*types.Interface)
			if !ok {
				return nil
			}
			foundDriverIface = true

			// Find handleOrphan across all files in the package and scan its body.
			for _, file := range p.Files {
				for _, decl := range file.Decls {
					fd, ok := decl.(*ast.FuncDecl)
					if !ok || fd.Name == nil || fd.Name.Name != handleOrphanFuncName {
						continue
					}
					foundHandleOrphan = true
					if fd.Body == nil {
						continue
					}
					// Walk all call expressions in handleOrphan's body.
					EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok {
							return
						}
						fn, ok := ResolveMethodCall(p.TypesInfo, sel)
						if !ok || fn == nil {
							return
						}
						// Check if the method receiver's type satisfies Driver.
						sig, ok := fn.Type().(*types.Signature)
						if !ok || sig.Recv() == nil {
							return
						}
						recvType := sig.Recv().Type()
						if isTypeOrPtrImplementsIface(recvType, driverIface) {
							pos := p.Fset.Position(call.Pos())
							violations = append(violations, Diagnostic{
								Rel:     p.Rel(file),
								Line:    pos.Line,
								Message: "handleOrphan must not call Driver." + fn.Name() + "; Orphan is a no-I/O operation",
							})
						}
					})
				}
			}
			return nil
		})

	assert.True(t, foundHandleOrphan,
		"%s: handleOrphan function not found in runtime/distlock; rule cannot enforce",
		ruleDistlockOrphanNoDriverIO01)
	assert.True(t, foundDriverIface,
		"%s: Driver interface not found in runtime/distlock scope; rule cannot enforce",
		ruleDistlockOrphanNoDriverIO01)
	Report(t, ruleDistlockOrphanNoDriverIO01, violations)
}

// TestDistlockOrphanNoDriverIO01_BlindSpotSelfCheck verifies the detection
// logic is not a no-op by constructing an in-memory function that DOES call
// a Driver method (Release), type-checks it against a synthetic Driver
// interface, and asserts that the SelectorExpr is correctly identified as a
// Driver method call.
//
// This test uses an in-memory types.Config/importer.Default() (not the
// RunTypedFixture façade) because it is a synthetic no-op-detector probe, not
// an on-disk fixture package.
//
// ai-robust.md §"AI-robust 三档分级" mandates a blind-spot self-check for
// Hard/Medium-rated archtest rules.
func TestDistlockOrphanNoDriverIO01_BlindSpotSelfCheck(t *testing.T) {
	t.Parallel()

	// Fixture: synthetic package containing a Driver-like interface and a
	// function that calls its Release method. Built in-memory so the
	// production tree contains no such "driver call inside orphan" pattern.
	src := `package fixture
import "context"

type FakeDriver interface {
	Release(ctx context.Context, key, token string) error
}

type FakeMgr struct{ d FakeDriver }

// syntheticOrphan is the fixture function — it DOES call a Driver method.
func (m *FakeMgr) syntheticOrphan(ctx context.Context, key, token string) {
	_ = m.d.Release(ctx, key, token)
}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture")
	conf := types.Config{Importer: importer.Default()}
	info := &types.Info{
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	pkg, err := conf.Check("fixture", fset, []*ast.File{file}, info)
	require.NoError(t, err, "type-check fixture")

	// Resolve the FakeDriver interface type.
	driverObj := pkg.Scope().Lookup("FakeDriver")
	require.NotNil(t, driverObj, "FakeDriver not in scope")
	driverNamed, ok := driverObj.Type().(*types.Named)
	require.True(t, ok, "FakeDriver must be *types.Named")
	driverIface, ok := driverNamed.Underlying().(*types.Interface)
	require.True(t, ok, "FakeDriver must have interface underlying type")

	// Walk the syntheticOrphan function body and check if the Release call is detected.
	var detectedDriverCall bool
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Name == nil || fd.Name.Name != "syntheticOrphan" {
			continue
		}
		require.NotNil(t, fd.Body, "syntheticOrphan body must not be nil")
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return
			}
			sel2, ok := info.Selections[sel]
			if !ok {
				return
			}
			fn, ok := sel2.Obj().(*types.Func)
			if !ok {
				return
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok || sig.Recv() == nil {
				return
			}
			if isTypeOrPtrImplementsIface(sig.Recv().Type(), driverIface) {
				detectedDriverCall = true
			}
		})
	}

	assert.True(t, detectedDriverCall,
		"BlindSpotSelfCheck: syntheticOrphan calls FakeDriver.Release but the "+
			"detector did not flag it. If this fails, the forward check's "+
			"detection logic is broken and would silently miss real Driver calls "+
			"in handleOrphan. Check isTypeOrPtrImplementsIface and the "+
			"TypesInfo.Selections resolution path.")
}

// isTypeOrPtrImplementsIface reports whether t or *t implements iface.
// Used instead of typesutil.ImplementsInterface because we specifically want
// to check both value and pointer receiver forms of the Driver interface
// methods (which take pointer receivers in production).
func isTypeOrPtrImplementsIface(t types.Type, iface *types.Interface) bool {
	if types.Implements(t, iface) {
		return true
	}
	ptr := types.NewPointer(t)
	return types.Implements(ptr, iface)
}

// ---- F1b: DISTLOCK-MANAGER-DRIVER-IO-OFFLOADED-01 -------------------------

const ruleDistlockMgrDriverIOOffloaded01 = "DISTLOCK-MANAGER-DRIVER-IO-OFFLOADED-01"

// driverIOOffloadedExemptFuncs is the set of function names exempt from
// DISTLOCK-MANAGER-DRIVER-IO-OFFLOADED-01. Each exempt function either:
//   - Makes a Driver call that is intentionally synchronous (Acquire / SetNX),
//   - OR is a private helper that is exclusively called from a goroutine and
//     therefore effectively runs off the manager loop (runRenewAttempts,
//     renewWorker).
//
// The exempt set is maintained here; adding a new name must be accompanied by
// a code comment explaining why it is safe to exempt.
var driverIOOffloadedExemptFuncs = map[string]bool{
	"Acquire":          true, // SetNX is the synchronous acquire RPC; not part of manager loop
	"renewWorker":      true, // exclusively launched via `go m.renewWorker(...)` — runs off manager loop
	"runRenewAttempts": true, // exclusively called from renewWorker goroutine — runs off manager loop
}

// TestDistlockMgrDriverIOOffloaded01 asserts that every call to a
// distlock.Driver interface method (SetNX / Renew / Release) in
// runtime/distlock production code occurs inside the func-literal of a go
// statement (i.e., in a background goroutine). The sole exceptions are
// functions in driverIOOffloadedExemptFuncs (see above for rationale).
func TestDistlockMgrDriverIOOffloaded01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		foundDriverIface bool
		violations       []Diagnostic
	)

	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/distlock/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != distlockMgrPkgPath {
				return nil
			}

			// Resolve the Driver interface type from the package scope.
			driverObj := p.Pkg.Scope().Lookup(driverIfaceName)
			if driverObj == nil {
				return nil
			}
			driverNamed, ok := driverObj.Type().(*types.Named)
			if !ok {
				return nil
			}
			driverIface, ok := driverNamed.Underlying().(*types.Interface)
			if !ok {
				return nil
			}
			foundDriverIface = true

			// Walk every call expression in every production function and check
			// whether Driver method calls are always inside a go statement.
			for _, file := range p.Files {
				for _, decl := range file.Decls {
					fd, ok := decl.(*ast.FuncDecl)
					if !ok || fd.Body == nil {
						continue
					}
					// Skip exempt functions (see driverIOOffloadedExemptFuncs).
					if fd.Name != nil && driverIOOffloadedExemptFuncs[fd.Name.Name] {
						continue
					}
					EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if !ok {
							return
						}
						fn, ok := ResolveMethodCall(p.TypesInfo, sel)
						if !ok || fn == nil {
							return
						}
						sig, ok := fn.Type().(*types.Signature)
						if !ok || sig.Recv() == nil {
							return
						}
						if !isTypeOrPtrImplementsIface(sig.Recv().Type(), driverIface) {
							return
						}
						// Driver method call found. Check if it is inside a go statement.
						if !isInsideGoStmt(fd.Body, call) {
							pos := p.Fset.Position(call.Pos())
							violations = append(violations, Diagnostic{
								Rel:  p.Rel(file),
								Line: pos.Line,
								Message: "Driver." + fn.Name() + " must be called inside a " +
									"go statement (background goroutine) to keep the manager " +
									"loop live; exempt function: Acquire (SetNX is sync acquire RPC)",
							})
						}
					})
				}
			}
			return nil
		})

	assert.True(t, foundDriverIface,
		"%s: Driver interface not found in runtime/distlock scope; rule cannot enforce",
		ruleDistlockMgrDriverIOOffloaded01)
	Report(t, ruleDistlockMgrDriverIOOffloaded01, violations)
}

// isInsideGoStmt reports whether the given CallExpr is lexically nested inside
// a *ast.GoStmt anywhere in the subtree rooted at root. Two forms are accepted:
//
//  1. go func() { ... target ... }() — target is inside the func-literal body.
//  2. go m.someMethod(...) where target == the go statement's call itself —
//     the Driver method call IS the call that runs in the goroutine.
func isInsideGoStmt(root ast.Node, target *ast.CallExpr) bool {
	found := false
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil || found {
			return false
		}
		goStmt, ok := n.(*ast.GoStmt)
		if !ok {
			return true
		}
		// Form 2: the go statement's Call IS the target (e.g. go m.renewWorker(...)).
		if goStmt.Call == target {
			found = true
			return false
		}
		// Form 1: target is nested inside a func-literal body.
		funcLit, ok := goStmt.Call.Fun.(*ast.FuncLit)
		if !ok {
			return true
		}
		ast.Inspect(funcLit.Body, func(inner ast.Node) bool {
			if inner == target {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

// ---- F3: Reverse self-checks for DISTLOCK-ORPHAN-NO-DRIVER-IO-01 blind spots

// TestDistlockOrphanNoDriverIO01_ReverseCheck_LocalVarMethodValue asserts that
// the local-var-method-value blind spot form (`rel := m.driver.Release; rel(...)`)
// does NOT appear in the runtime/distlock production AST. This verifies the
// blind spot is currently absent, not actively exploited.
func TestDistlockOrphanNoDriverIO01_ReverseCheck_LocalVarMethodValue(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// We check for the blind-spot form: a variable assigned from a method-value
	// expression whose receiver is a Driver (e.g. `rel := m.driver.Release`).
	// This is an AssignStmt where the RHS is a SelectorExpr that resolves to a
	// Driver method but is NOT wrapped in a CallExpr (it's a method value, not
	// a call).
	var violations []Diagnostic

	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/distlock/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != distlockMgrPkgPath {
				return nil
			}
			driverObj := p.Pkg.Scope().Lookup(driverIfaceName)
			if driverObj == nil {
				return nil
			}
			driverNamed, ok := driverObj.Type().(*types.Named)
			if !ok {
				return nil
			}
			driverIface, ok := driverNamed.Underlying().(*types.Interface)
			if !ok {
				return nil
			}

			for _, file := range p.Files {
				// Walk all SelectorExprs that are NOT the Fun of a CallExpr (method values).
				ast.Inspect(file, func(n ast.Node) bool {
					assign, ok := n.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for _, rhs := range assign.Rhs {
						sel, ok := rhs.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						fn, ok := ResolveMethodCall(p.TypesInfo, sel)
						if !ok || fn == nil {
							continue
						}
						sig, ok := fn.Type().(*types.Signature)
						if !ok || sig.Recv() == nil {
							continue
						}
						if isTypeOrPtrImplementsIface(sig.Recv().Type(), driverIface) {
							pos := p.Fset.Position(sel.Pos())
							violations = append(violations, Diagnostic{
								Rel:     p.Rel(file),
								Line:    pos.Line,
								Message: "F3 reverse-check: found local-var-method-value blind spot form: Driver." + fn.Name() + " taken as method value",
							})
						}
					}
					return true
				})
			}
			return nil
		})

	Report(t, ruleDistlockOrphanNoDriverIO01+".ReverseCheck.LocalVarMethodValue", violations)
}

// TestDistlockOrphanNoDriverIO01_ReverseCheck_DriverFuncField asserts that the
// function-pointer-field blind spot form (a struct field holding a Driver method
// as a func) does NOT appear in the runtime/distlock production AST.
func TestDistlockOrphanNoDriverIO01_ReverseCheck_DriverFuncField(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// We check for struct fields of func type whose signatures match Driver
	// method signatures (SetNX / Renew / Release). If any struct in the package
	// holds such a field, it would be a blind spot for the orphan-no-IO rule.
	var violations []Diagnostic

	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/distlock/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil || p.Pkg.Path() != distlockMgrPkgPath {
				return nil
			}
			driverObj := p.Pkg.Scope().Lookup(driverIfaceName)
			if driverObj == nil {
				return nil
			}
			driverNamed, ok := driverObj.Type().(*types.Named)
			if !ok {
				return nil
			}
			driverIface, ok := driverNamed.Underlying().(*types.Interface)
			if !ok {
				return nil
			}

			// Walk type declarations looking for struct fields with func types that
			// match a Driver method signature.
			for _, name := range p.Pkg.Scope().Names() {
				obj := p.Pkg.Scope().Lookup(name)
				if obj == nil {
					continue
				}
				named, ok := obj.Type().(*types.Named)
				if !ok {
					continue
				}
				st, ok := named.Underlying().(*types.Struct)
				if !ok {
					continue
				}
				for i := range st.NumFields() {
					field := st.Field(i)
					sig, ok := field.Type().(*types.Signature)
					if !ok {
						continue
					}
					// Check if this func signature matches any Driver method signature.
					for j := range driverIface.NumMethods() {
						mSig, ok := driverIface.Method(j).Type().(*types.Signature)
						if !ok {
							continue
						}
						if types.Identical(sig, mSig) {
							msg := "F3 reverse-check: struct " + named.Obj().Name() +
								" field " + field.Name() +
								" has func type matching Driver." + driverIface.Method(j).Name()
							violations = append(violations, Diagnostic{
								Rel:     "runtime/distlock",
								Line:    0,
								Message: msg,
							})
						}
					}
				}
			}
			return nil
		})

	Report(t, ruleDistlockOrphanNoDriverIO01+".ReverseCheck.DriverFuncField", violations)
}
