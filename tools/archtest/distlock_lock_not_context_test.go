package archtest

// INVARIANT: DISTLOCK-LOCK-NOT-CONTEXT-01
//
// runtime/distlock.Lock MUST NOT implement context.Context.
//
// Background: prior to PR-#20-FIX (commit hash recorded in ADR
// docs/architecture/202605200000-adr-distlock-lock-as-resource.md),
// distlock.Locker.Acquire returned a context.Context derived from the
// caller-supplied ctx. Caller-ctx cancellation propagated to the lock
// context, conflating request lifecycle with lock ownership and causing
// renewal to continue after caller-ctx cancel (GH #20). The fix returns
// a sealed *Lock value that intentionally OMITS Deadline() and Err() so
// it does not satisfy context.Context. Call sites like
// db.QueryContext(lock, ...) fail at compile time — type-system Hard
// against caller misuse.
//
// AI-robust evaluation:
//   - Downstream defense (caller cannot pass *Lock where context.Context
//     is required): Hard, enforced by Go type system. Cannot be
//     bypassed without a code change in the caller.
//   - Upstream defense (implementer cannot add Deadline()/Err() methods
//     to *Lock and accidentally satisfy context.Context): Medium,
//     enforced by this archtest. A package-internal mutation that adds
//     both methods is detectable only by this static check — there is
//     no sealed-interface marker that would make the mutation
//     impossible at compile time.
//
// Hard-upstream upgrade path: a sealed-interface wrapper of *Lock would make
// the type-system reject added Deadline()/Err() methods at compile time.
// Per ai-robust.md §"Funnel 双向锁评级", until then the Medium+Hard posture
// documented in ADR 202605200000-adr-distlock-lock-as-resource.md
// §"Enforcement" is the active line.
//
// Reverse self-check (TestDistlockLockNotContext01_BlindSpotSelfCheck)
// builds an in-memory fixture type that DOES implement context.Context
// and asserts ImplementsInterface returns true on it. This proves the
// detection logic is not a no-op (e.g. if typesutil.ImplementsInterface
// regressed to always return false, the forward test would still pass
// silently — the reverse test catches that bug class).
//
// ref: ADR 202605200000-adr-distlock-lock-as-resource.md
// ref: GH #20 DISTLOCK-RENEW-CALLER-CONTEXT-01
// ref: .claude/rules/gocell/ai-robust.md "Funnel 双向锁评级" /
//      "盲区清单+反向自检测试"

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
	ruleDistlockLockNotContext01 = "DISTLOCK-LOCK-NOT-CONTEXT-01"
	distlockPkgPath              = "github.com/ghbvf/gocell/runtime/distlock"
	contextPkgPath               = "context"
	lockTypeName                 = "Lock"
	contextTypeName              = "Context"
)

func TestDistlockLockNotContext01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		foundLockType bool
		foundCtxIface bool
		implements    bool
	)

	Run(t, Typed(TypedOpts{Tests: false}, []string{"./runtime/distlock/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != distlockPkgPath {
				return nil
			}
			obj := p.Pkg.Scope().Lookup(lockTypeName)
			if obj == nil {
				return nil
			}
			lockType, ok := obj.Type().(*types.Named)
			if !ok {
				return nil
			}
			foundLockType = true

			ctxIface := resolveContextInterface(p.Pkg.Imports())
			if ctxIface == nil {
				return nil
			}
			foundCtxIface = true

			implements = typesutil.ImplementsInterface(lockType, ctxIface)
			return nil
		})

	assert.True(t, foundLockType,
		"%s: runtime/distlock.Lock type not found; rule cannot enforce",
		ruleDistlockLockNotContext01)
	assert.True(t, foundCtxIface,
		"%s: context.Context interface not resolvable via distlock imports; rule cannot enforce",
		ruleDistlockLockNotContext01)
	assert.False(t, implements,
		"%s: *runtime/distlock.Lock must NOT implement context.Context; "+
			"adding Deadline()/Err() methods would let callers pass *Lock to "+
			"db.QueryContext / http.NewRequestWithContext / etc., reintroducing "+
			"the misuse class identified in GH #20. See ADR "+
			"docs/architecture/202605200000-adr-distlock-lock-as-resource.md.",
		ruleDistlockLockNotContext01)
}

// TestDistlockLockNotContext01_BlindSpotSelfCheck verifies that the
// detection logic is not a no-op by constructing an in-memory fixture
// type that DOES implement context.Context and asserting that
// ImplementsInterface reports it. If a future refactor were to regress
// typesutil.ImplementsInterface to always return false, the forward test
// above would still pass silently — this reverse test catches that.
//
// ai-robust.md §"AI-robust 三档分级" mandates a blind-spot self-check
// for Hard/Medium-rated archtest rules.
func TestDistlockLockNotContext01_BlindSpotSelfCheck(t *testing.T) {
	t.Parallel()

	// Fixture: a type whose pointer receiver implements every method of
	// context.Context. Built in-memory so the production tree contains no
	// such "almost a Context" type.
	src := `package fixture
import "time"
type FakeLock struct{}
func (*FakeLock) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*FakeLock) Done() <-chan struct{}        { return nil }
func (*FakeLock) Err() error                   { return nil }
func (*FakeLock) Value(key any) any            { return nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	require.NoError(t, err, "parse fixture")
	conf := types.Config{Importer: importer.Default()}
	pkg, err := conf.Check("fixture", fset, []*ast.File{file}, nil)
	require.NoError(t, err, "type-check fixture")

	obj := pkg.Scope().Lookup("FakeLock")
	require.NotNil(t, obj, "FakeLock not in fixture scope")
	fakeType, ok := obj.Type().(*types.Named)
	require.True(t, ok, "FakeLock should be *types.Named")

	// Resolve context.Context via stdlib importer.
	ctxPkg, err := conf.Importer.Import(contextPkgPath)
	require.NoError(t, err, "import context package")
	ctxObj := ctxPkg.Scope().Lookup(contextTypeName)
	require.NotNil(t, ctxObj, "context.Context not in scope")
	named, ok := ctxObj.Type().(*types.Named)
	require.True(t, ok)
	ctxIface, ok := named.Underlying().(*types.Interface)
	require.True(t, ok)

	assert.True(t,
		typesutil.ImplementsInterface(fakeType, ctxIface),
		"BlindSpotSelfCheck: fixture FakeLock implements every method of "+
			"context.Context; if ImplementsInterface returns false here, the "+
			"forward check is broken and would let *Lock grow Context methods "+
			"undetected. Check typesutil.ImplementsInterface implementation.")
}

// resolveContextInterface walks the supplied import set and returns the
// context.Context interface type, or nil if not reachable. Shared by the
// forward check and any caller that needs the canonical interface object.
func resolveContextInterface(imports []*types.Package) *types.Interface {
	for _, imp := range imports {
		if imp.Path() != contextPkgPath {
			continue
		}
		ctxObj := imp.Scope().Lookup(contextTypeName)
		if ctxObj == nil {
			continue
		}
		named, ok := ctxObj.Type().(*types.Named)
		if !ok {
			continue
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			continue
		}
		return iface
	}
	return nil
}
