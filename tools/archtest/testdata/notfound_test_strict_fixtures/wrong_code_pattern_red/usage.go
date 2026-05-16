// Package wrong_code_pattern_red proves that a _NotFound test calling
// errcodetest.AssertCode with a typed errcode.Code constant that does NOT
// match ^ERR_.*_NOT_FOUND$ is rejected (the test is testing NotFound but
// asserts a different category). Line 15 is flagged.
package wrong_code_pattern_red

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestFoo_NotFound(t *testing.T) {
	err := errors.New("simulated")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}
