// Package compliant_trun_green proves that a t.Run("Sub_NotFound", ...) table
// case calling errcodetest.AssertCode with a typed errcode.Err*NotFound is
// accepted: 0 violations expected.
package compliant_trun_green

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestRepo_Errors(t *testing.T) {
	t.Run("GetByKey_NotFound", func(t *testing.T) {
		err := errors.New("simulated")
		errcodetest.AssertCode(t, err, errcode.ErrConfigRepoNotFound)
	})
}
