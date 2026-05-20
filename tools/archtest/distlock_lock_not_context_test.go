package archtest

// invariants:
//   - INVARIANT: DISTLOCK-LOCK-NOT-CONTEXT-01
//
// distlock_lock_not_context_test.go enforces DISTLOCK-LOCK-NOT-CONTEXT-01:
// the type runtime/distlock.Lock MUST NOT implement context.Context.
//
// Background: prior to PR-#20-FIX, distlock.Locker.Acquire returned a
// context.Context derived from the caller-supplied ctx. Caller-ctx
// cancellation propagated to the lock context, which conflated request
// lifecycle with lock ownership and caused renewal to continue after
// caller-ctx cancel (GH #20). The fix (ADR
// docs/architecture/202605200000-adr-distlock-lock-as-resource.md) returns
// a sealed *Lock value that intentionally OMITS Deadline() and Err() so
// it does not satisfy the context.Context interface. As a result, calls
// such as db.QueryContext(lock, ...) fail at compile time — Hard
// enforcement at the type-system layer.
//
// This archtest is the second line of defense: it would catch a future
// commit that adds Deadline() (time.Time, bool) or Err() error methods
// to *Lock and accidentally satisfies context.Context.
//
// AI-rebust: Hard (type-system primary defense + archtest regression guard).
// "single sanctioned holder" form: there is exactly one path by which Lock
// could become a context.Context (adding both Deadline and Err methods);
// this test forbids that path via types.Implements.
//
// ref: ADR 202605200000-adr-distlock-lock-as-resource.md
// ref: GH #20 DISTLOCK-RENEW-CALLER-CONTEXT-01

import (
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"

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

	RunTyped(t, TypedOpts{Tests: false}, []string{"./runtime/distlock/..."},
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

			// Find context.Context interface type via *Lock's reachable imports.
			// Pass.Pkg.Imports() exposes the package's import set; context is
			// imported by lock.go (for context.WithoutCancel + the Value()
			// method's argument type).
			var ctxIface *types.Interface
			for _, imp := range p.Pkg.Imports() {
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
				ctxIface = iface
				break
			}
			if ctxIface == nil {
				return nil
			}
			foundCtxIface = true

			// Hard check: neither Lock nor *Lock may implement context.Context.
			// If either does, Lock has accidentally grown Deadline()/Err()
			// methods (or context.Context itself shrank) — either way, the
			// type-system defense against caller misuse has been weakened.
			// ImplementsInterface checks the value-or-pointer method set.
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
