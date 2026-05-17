// Package nested_funclit_red proves that a funnel call inside a NESTED
// *ast.FuncLit (dead closure) is NOT credited to the outer _NotFound
// test. Without the EachInSubtreeStopAt boundary, the recursive walker
// would visit the inner closure's funnel call and falsely mark the
// outer body satisfied. The test function below has zero ACTUAL
// funnel-call presence — the funnel call is buried in a closure that
// is assigned and never invoked.
// fn at line 17; the violation is emitted at fn.Pos().
package nested_funclit_red

import (
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"testing"
)

func TestFoo_NotFound(t *testing.T) {
	// Dead closure — never invoked. The funnel call inside is unreachable
	// from this test's actual execution path. Crediting it would be the
	// exact fail-open the rule exists to forbid.
	_ = func() {
		errcodetest.AssertCode(t, nil, errcode.ErrSessionNotFound)
	}
	// No runnable funnel call at the outer body level.
}
