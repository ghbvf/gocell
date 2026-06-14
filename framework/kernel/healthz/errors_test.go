package healthz

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

func TestErrDuplicateProbe_IsErrcode(t *testing.T) {
	t.Parallel()

	var ec *errcode.Error
	if !errors.As(ErrDuplicateProbe, &ec) {
		t.Fatalf("ErrDuplicateProbe is not *errcode.Error")
	}
	if ec.Code != errcode.ErrConflict {
		t.Errorf("ErrDuplicateProbe.Code = %v, want ErrConflict", ec.Code)
	}
}

func TestErrInvalidProbeName_IsErrcode(t *testing.T) {
	t.Parallel()

	var ec *errcode.Error
	if !errors.As(ErrInvalidProbeName, &ec) {
		t.Fatalf("ErrInvalidProbeName is not *errcode.Error")
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("ErrInvalidProbeName.Code = %v, want ErrValidationFailed", ec.Code)
	}
}
