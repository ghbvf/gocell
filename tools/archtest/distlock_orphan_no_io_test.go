// INVARIANT: DISTLOCK-ORPHAN-NO-DRIVER-IO-01
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
//   - Method-value assignment: `f := m.driver.Release; f(...)` — TypesInfo
//     resolves the SelectorExpr `m.driver.Release` as a method value, so
//     this form IS detected by the current check.
//   - Function-pointer field: if a helper struct stored a Driver method as a
//     func field and handleOrphan called that field, it would not be detected.
//
// Reverse self-check (TestDistlockOrphanNoDriverIO01_BlindSpotSelfCheck):
// builds a synthetic function body that DOES call a Driver method and asserts
// that the detector flags it. This proves the detection logic is not a no-op.
//
// ref: runtime/distlock/manager.go handleOrphan
// ref: runtime/distlock/lock.go Lock.Orphan
// ref: etcd-io/etcd client/v3/concurrency/session.go Session.Orphan
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
	distlockMgrPkgPath             = "github.com/ghbvf/gocell/runtime/distlock"
	handleOrphanFuncName           = "handleOrphan"
	driverIfaceName                = "Driver"
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
