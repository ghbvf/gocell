// Package basic_lit_expected_red proves that a _NotFound test calling
// errcodetest.AssertCode with a string literal expected (rather than a typed
// errcode.Code SelectorExpr) is rejected, even when the literal happens to
// match the NotFound pattern. Line 15 is flagged.
package basic_lit_expected_red

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestFoo_NotFound(t *testing.T) {
	err := errors.New("simulated")
	errcodetest.AssertCode(t, err, errcode.Code("ERR_SESSION_NOT_FOUND"))
}
