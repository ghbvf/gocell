// Package non_inline_body_red proves that
// `t.Run("..._NotFound", helperFunc)` — body is a function reference,
// not an inline FuncLit — is rejected (fail-closed). The static AST
// scan cannot verify funnel-call presence across a function-reference
// boundary; forcing inline FuncLit is the only sanctioned shape.
// t.Run at line 17 — the violation is emitted at call.Pos().
package non_inline_body_red

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestRepo_Errors(t *testing.T) {
	t.Run("GetByKey_NotFound", runGetByKeyNotFound) //nolint:thelper // fixture
}

func runGetByKeyNotFound(t *testing.T) {
	// Even though this helper DOES call the funnel, the call site at
	// t.Run is the rule's anchor — non-inline body fails closed.
	errcodetest.AssertCode(t, nil, errcode.ErrConfigRepoNotFound)
}
