package errcode_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// TestRegistrationSentinels asserts the three dedicated runtime
// contract-registration sentinels (303-US2, #2233) exist, carry the expected
// wire codes, and map to the documented Kind/HTTP status. Mirrors
// TestSagaSentinels: dedicated codes let operator routing distinguish
// registration failure modes from generic validation/conflict signals.
func TestRegistrationSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code       errcode.Code
		wantString string
		wantKind   errcode.Kind
		wantStatus int
	}{
		{errcode.ErrRegistrationInvalidTransition, "ERR_REGISTRATION_INVALID_TRANSITION", errcode.KindInvalid, http.StatusBadRequest},
		{errcode.ErrRegistrationNotFound, "ERR_REGISTRATION_NOT_FOUND", errcode.KindNotFound, http.StatusNotFound},
		{errcode.ErrRegistrationDuplicate, "ERR_REGISTRATION_DUPLICATE", errcode.KindConflict, http.StatusConflict},
	}
	for _, tc := range cases {
		if got := string(tc.code); got != tc.wantString {
			t.Errorf("code %v: got string %q, want %q", tc.code, got, tc.wantString)
		}
		err := errcode.New(tc.wantKind, tc.code, "registry: probe message")
		var ec *errcode.Error
		if !errors.As(err, &ec) {
			t.Fatalf("code %v: errors.As(*errcode.Error) failed", tc.code)
		}
		if ec.Kind != tc.wantKind {
			t.Errorf("code %v: Kind=%v, want %v", tc.code, ec.Kind, tc.wantKind)
		}
		if ec.Code != tc.code {
			t.Errorf("code %v: ec.Code=%v, want %v", tc.code, ec.Code, tc.code)
		}
		if got := ec.Status(); got != tc.wantStatus {
			t.Errorf("code %v: Status()=%d, want %d", tc.code, got, tc.wantStatus)
		}
	}
}
