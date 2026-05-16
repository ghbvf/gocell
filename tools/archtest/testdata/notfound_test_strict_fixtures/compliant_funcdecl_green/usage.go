// Package compliant_funcdecl_green proves that a FuncDecl whose name ends in
// _NotFound calling errcodetest.AssertCode with a typed errcode.Err*NotFound
// SelectorExpr is accepted: 0 violations expected.
package compliant_funcdecl_green

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestFoo_NotFound(t *testing.T) {
	err := errors.New("simulated")
	errcodetest.AssertCode(t, err, errcode.ErrSessionNotFound)
}
