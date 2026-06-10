//go:build archtest

//   - INVARIANT: DISTLOCK-ORPHAN-NO-DRIVER-IO-01
//   - INVARIANT: DISTLOCK-MANAGER-DRIVER-IO-OFFLOADED-01
//
// # DISTLOCK-ORPHAN-NO-DRIVER-IO-01
//
// Within runtime/distlock, the function handleOrphan — AND every same-package
// function in its transitive call closure (e.g. detachLock, markCause) — MUST
// NOT call any method of the distlock.Driver interface (SetNX / Renew / Release).
//
// Rationale: Lock.Orphan() is designed to stop lease renewal WITHOUT any
// backend I/O so that callers can hand off the lock during graceful shutdown
// even when the backend (Redis) is unreachable. If handleOrphan — or any helper
// it reaches at any depth — were to call Driver.SetNX / Driver.Renew /
// Driver.Release, the no-IO contract would be silently violated: shutdown could
// block or fail exactly when it must not. detachLock is shared with handleRemove,
// so a Driver call added there (even one offloaded to a goroutine) would leak
// I/O onto the orphan path — hence the scan flags ANY Driver call in any
// reachable body, regardless of go-statement nesting.
//
// Detection: build the full same-package transitive call closure rooted at
// handleOrphan (BFS worklist; callees resolved via TypesInfo.ObjectOf, covering
// both method selectors and bare same-package func idents), then scan every body
// in the closure. A Driver call is identified by resolving the *ast.SelectorExpr
// call-target to its *types.Func and comparing its receiver interface type
// against distlock.Driver. The closure is derived generically, so it tracks
// handleOrphan's helper set automatically at any depth.
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
//   - Deep indirect dispatch via plain call graph: CLOSED (F3). The scan walks
//     the full same-package transitive closure rooted at handleOrphan, so a
//     Driver call added at any depth (handleOrphan → detachLock → deeperHelper →
//     Driver) is flagged, not just 1-hop. The closure stops at the package
//     boundary; a cross-package helper that itself calls Driver is out of scope
//     (and structurally impossible here — Driver lives in this package and
//     handleOrphan's reachable helpers are all package-local).
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
//   - TestDistlockOrphanNoDriverIO01_TransitiveClosureSelfCheck: builds a
//     synthetic 3-deep call chain (root → mid → leaf→Driver) and asserts the
//     closure walk reaches the leaf and flags it. Proves the >1-hop traversal is
//     not a no-op (guards the deep-indirect closure claim above).
//   - TestDistlockOrphanNoDriverIO01_ReverseCheck_LocalVarMethodValue: asserts
//     the local-var-method-value blind spot form does NOT appear in production.
//   - TestDistlockOrphanNoDriverIO01_ReverseCheck_DriverFuncField: asserts the
//     function-pointer-field blind spot form does NOT appear in production.
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

	"github.com/ghbvf/gocell/tools/typesutil"
)

const (
	ruleDistlockOrphanNoDriverIO01 = "DISTLOCK-ORPHAN-NO-DRIVER-IO-01"
	// distlockMgrPkgPath is a GoCell platform symbol path, anchored to
	// PlatformModulePath so a module rename / /v2 bump updates exactly one
	// place and ARCHTEST-MODULE-PATH-FUNNEL-01 stays green (no bare literal).
	distlockMgrPkgPath   = PlatformModulePath + "/runtime/distlock"
	handleOrphanFuncName = "handleOrphan"
	detachLockFuncName   = "detachLock"
	driverIfaceName      = "Driver"
)

// TestDistlockOrphanNoDriverIO01 asserts that handleOrphan in runtime/distlock
// — AND every same-package function in its transitive call closure — does not
// call any method of the distlock.Driver interface. The transitive-closure walk
// (F3) closes the indirect-dispatch blind spot: a Driver call added to any
// helper reachable from handleOrphan at any depth (e.g. the shared detachLock
// helper, even one offloaded to a goroutine) would silently give the orphan path
// backend I/O. The closure is derived generically from type-resolved callees, so
// it auto-extends if handleOrphan gains helpers at any depth.
func TestDistlockOrphanNoDriverIO01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		foundHandleOrphan bool
		foundDriverIface  bool
		scannedDetachLock bool
		violations        []Diagnostic
	)

	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/distlock/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != distlockMgrPkgPath {
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
			foundDriverIface = true

			type funcEntry struct {
				fd   *ast.FuncDecl
				file *ast.File
			}
			funcByName := map[string]funcEntry{}
			for _, file := range p.Files {
				EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
					if fd.Name != nil {
						funcByName[fd.Name.Name] = funcEntry{fd: fd, file: file}
					}
				})
			}

			orphan, ok := funcByName[handleOrphanFuncName]
			if !ok || orphan.fd.Body == nil {
				return nil
			}
			foundHandleOrphan = true

			scanBody := func(name string, fe funcEntry) {
				EachInSubtree[ast.CallExpr](fe.fd.Body, func(call *ast.CallExpr) {
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
					if isTypeOrPtrImplementsIface(sig.Recv().Type(), driverIface) {
						pos := p.Fset.Position(call.Pos())
						violations = append(violations, Diagnostic{
							Rel:  p.Rel(fe.file),
							Line: pos.Line,
							Message: name + " must not call Driver." + fn.Name() +
								"; handleOrphan's transitive same-package closure is a no-I/O operation",
						})
					}
				})
			}

			samePkgCallee := func(call *ast.CallExpr) string {
				var id *ast.Ident
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					id = fun.Sel
				case *ast.Ident:
					id = fun
				default:
					return ""
				}
				fn, ok := p.TypesInfo.ObjectOf(id).(*types.Func)
				if !ok || fn.Pkg() == nil || fn.Pkg() != p.Pkg {
					return ""
				}
				return fn.Name()
			}

			scanSet := map[string]funcEntry{}
			queue := []string{handleOrphanFuncName}
			for len(queue) > 0 {
				name := queue[0]
				queue = queue[1:]
				if _, seen := scanSet[name]; seen {
					continue
				}
				fe, ok := funcByName[name]
				if !ok || fe.fd.Body == nil {
					continue
				}
				scanSet[name] = fe
				EachInSubtree[ast.CallExpr](fe.fd.Body, func(call *ast.CallExpr) {
					if callee := samePkgCallee(call); callee != "" {
						queue = append(queue, callee)
					}
				})
			}

			for name, fe := range scanSet {
				if name == detachLockFuncName {
					scannedDetachLock = true
				}
				scanBody(name, fe)
			}
			return nil
		})

	assert.True(t, foundHandleOrphan,
		"%s: handleOrphan function not found in runtime/distlock; rule cannot enforce",
		ruleDistlockOrphanNoDriverIO01)
	assert.True(t, foundDriverIface,
		"%s: Driver interface not found in runtime/distlock scope; rule cannot enforce",
		ruleDistlockOrphanNoDriverIO01)
	// detachLock is handleOrphan's known helper; if the 1-hop derivation stops
	// reaching it, the indirect-dispatch coverage silently regressed.
	assert.True(t, scannedDetachLock,
		"%s: expected the 1-hop helper scan to include %q (handleOrphan's heap-detach helper); "+
			"if handleOrphan no longer calls it, update this guard",
		ruleDistlockOrphanNoDriverIO01, detachLockFuncName)
	Report(t, ruleDistlockOrphanNoDriverIO01, violations)
}

// TestDistlockOrphanNoDriverIO01_BlindSpotSelfCheck verifies the detection
// logic is not a no-op by constructing an in-memory function that DOES call
// a Driver method (Release), type-checks it against a synthetic Driver
// interface, and asserts that the SelectorExpr is correctly identified as a
// Driver method call.
//
// This test uses an in-memory types.Config/importer.Default() (not the
// Run(t, Fixture(...)) façade) because it is a synthetic no-op-detector probe, not
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
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name == nil || fd.Name.Name != "syntheticOrphan" {
			return
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
	})

	assert.True(t, detectedDriverCall,
		"BlindSpotSelfCheck: syntheticOrphan calls FakeDriver.Release but the "+
			"detector did not flag it. If this fails, the forward check's "+
			"detection logic is broken and would silently miss real Driver calls "+
			"in handleOrphan. Check isTypeOrPtrImplementsIface and the "+
			"TypesInfo.Selections resolution path.")
}

// TestDistlockOrphanNoDriverIO01_TransitiveClosureSelfCheck proves the
// transitive-closure walk is not a no-op for deep (>1-hop) chains. It builds a
// synthetic 3-deep call chain root → mid → leaf where leaf calls a Driver
// method, runs the same BFS closure + Driver-detection logic the forward check
// uses, and asserts (a) the walk reaches leaf 2 hops from root and (b) the
// leaf's Driver call is flagged. Without a working >1-hop traversal the
// deep-indirect blind-spot closure claim in the package doc would be false.
func TestDistlockOrphanNoDriverIO01_TransitiveClosureSelfCheck(t *testing.T) {
	t.Parallel()

	src := `package fixture
import "context"

type FakeDriver interface {
	Release(ctx context.Context, key, token string) error
}

type FakeMgr struct{ d FakeDriver }

// root → mid → leaf; only leaf (2 hops down) calls a Driver method.
func (m *FakeMgr) root(ctx context.Context) { m.mid(ctx) }
func (m *FakeMgr) mid(ctx context.Context)  { m.leaf(ctx) }
func (m *FakeMgr) leaf(ctx context.Context) { _ = m.d.Release(ctx, "k", "t") }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture")
	info := &types.Info{
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("fixture", fset, []*ast.File{file}, info)
	require.NoError(t, err, "type-check fixture")

	driverObj := pkg.Scope().Lookup("FakeDriver")
	require.NotNil(t, driverObj, "FakeDriver not in scope")
	driverNamed, ok := driverObj.Type().(*types.Named)
	require.True(t, ok, "FakeDriver must be *types.Named")
	driverIface, ok := driverNamed.Underlying().(*types.Interface)
	require.True(t, ok, "FakeDriver must have interface underlying type")

	funcByName := map[string]*ast.FuncDecl{}
	EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
		if fd.Name != nil {
			funcByName[fd.Name.Name] = fd
		}
	})

	samePkgCallee := func(call *ast.CallExpr) string {
		var id *ast.Ident
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			id = fun.Sel
		case *ast.Ident:
			id = fun
		default:
			return ""
		}
		fn, ok := info.ObjectOf(id).(*types.Func)
		if !ok || fn.Pkg() == nil || fn.Pkg() != pkg {
			return ""
		}
		return fn.Name()
	}

	// BFS closure from root — mirrors the forward check's traversal.
	closure := map[string]bool{}
	reachedLeaf := false
	queue := []string{"root"}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if closure[name] {
			continue
		}
		fd, ok := funcByName[name]
		if !ok || fd.Body == nil {
			continue
		}
		closure[name] = true
		if name == "leaf" {
			reachedLeaf = true
		}
		EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
			if c := samePkgCallee(call); c != "" {
				queue = append(queue, c)
			}
		})
	}
	require.True(t, reachedLeaf,
		"TransitiveClosureSelfCheck: closure walk did not reach leaf (2 hops from "+
			"root); the >1-hop traversal is broken and deep Driver calls would be missed")

	// Scan the closure for Driver calls; the leaf's Release must be flagged.
	detected := false
	for name := range closure {
		EachInSubtree[ast.CallExpr](funcByName[name].Body, func(call *ast.CallExpr) {
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
				detected = true
			}
		})
	}
	assert.True(t, detected,
		"TransitiveClosureSelfCheck: leaf calls FakeDriver.Release 2 hops from root "+
			"but the closure scan did not flag it; the deep-indirect closure claim is false")
}

// isTypeOrPtrImplementsIface reports whether t or *t implements iface.
// Delegates to typesutil.ImplementsInterface which checks both value and
// pointer receiver forms — identical semantic to the original inline check.
func isTypeOrPtrImplementsIface(t types.Type, iface *types.Interface) bool {
	return typesutil.ImplementsInterface(t, iface)
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

	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/distlock/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if p.Pkg.Path() != distlockMgrPkgPath {
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
			foundDriverIface = true

			for _, file := range p.Files {
				EachInChildren[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
					if fd.Body == nil {
						return
					}

					if fd.Name != nil && driverIOOffloadedExemptFuncs[fd.Name.Name] {
						return
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
				})
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
	_, found := FindFirstInSubtree[ast.GoStmt](root, func(gs *ast.GoStmt) bool {
		// Form 2: the go statement's Call IS the target (e.g. go m.renewWorker(...)).
		if gs.Call == target {
			return true
		}
		// Form 1: target is nested inside a func-literal body.
		funcLit, ok := gs.Call.Fun.(*ast.FuncLit)
		if !ok {
			return false
		}
		_, nested := FindFirstInSubtree[ast.CallExpr](funcLit.Body, func(call *ast.CallExpr) bool {
			return call == target
		})
		return nested
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

	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/distlock/..."}),
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
				EachInSubtree[ast.AssignStmt](file, func(assign *ast.AssignStmt) {
					for _, rhs := range assign.Rhs {
						// Only flag direct (top-level) SelectorExpr in RHS — the
						// method-value form `f := receiver.Method`. Nested selectors
						// inside a call expression (e.g. `f := pkg.Func()`) are NOT
						// method-value blind spots, so we restrict to rhs itself.
						// Compare End() positions: the rhs CallExpr ends after ')' but
						// the nested Fun SelectorExpr ends after the method name, so
						// they differ; a direct SelectorExpr rhs has matching End().
						EachInSubtree[ast.SelectorExpr](rhs, func(sel *ast.SelectorExpr) {
							if sel.End() != rhs.End() {
								return // skip nested SelectorExprs
							}
							fn, ok := ResolveMethodCall(p.TypesInfo, sel)
							if !ok || fn == nil {
								return
							}
							sig, ok := fn.Type().(*types.Signature)
							if !ok || sig.Recv() == nil {
								return
							}
							if isTypeOrPtrImplementsIface(sig.Recv().Type(), driverIface) {
								pos := p.Fset.Position(sel.Pos())
								violations = append(violations, Diagnostic{
									Rel:     p.Rel(file),
									Line:    pos.Line,
									Message: "F3 reverse-check: found local-var-method-value blind spot form: Driver." + fn.Name() + " taken as method value",
								})
							}
						})
					}
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

	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/distlock/..."}),
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
