package taggrouploopfixtures

import (
	"testing"

	"github.com/ghbvf/gocell/tools/archtest"
)

// red_scope_var_indirection is the F2 scope-var-indirection bypass: the typed
// scope is bound to a local var (`scope := Typed(...)`), then the for-range over
// KnownNonDefaultTags() drives archtest.Run(t, scope, ...) — at the Run call site
// the 2nd arg is an *ast.Ident, not a direct constructor CallExpr. Because each
// loop iteration still re-runs Run with the same typed scope (a per-iteration
// packages.Load through the SharedResolver cache key bound at scope-construction
// time), the cumulative-RSS pathology applies. The detector must resolve the
// scope Ident's object back to its Typed(...) binding (collectTypedScopeBoundObjects)
// and catch it — one level of indirection past red_panic_invariants_style.
func _(t *testing.T) {
	scope := archtest.Typed(archtest.TypedOpts{Tags: []string{"prod"}}, []string{"./..."})
	for range archtest.KnownNonDefaultTags() {
		_ = archtest.Run(t, scope,
			func(p *archtest.Pass) []archtest.Diagnostic { return nil })
	}
}
