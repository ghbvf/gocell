package errcode_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestSagaSentinels asserts the three dedicated saga journal sentinels exist,
// carry the expected wire codes, and pair correctly with the kernel/saga/journal
// errors documented in interface.go. Introduced by PR-04 (#959) — replaces the
// PR-02 reuse of ErrValidationFailed / ErrConflict from kernel/saga/journal so
// operator routing can distinguish saga failure modes from generic validation.
func TestSagaSentinels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		code       errcode.Code
		wantString string
		wantKind   errcode.Kind
		wantStatus int
	}{
		{errcode.ErrSagaNotFound, "ERR_SAGA_NOT_FOUND", errcode.KindNotFound, http.StatusNotFound},
		{errcode.ErrSagaStaleLease, "ERR_SAGA_STALE_LEASE", errcode.KindConflict, http.StatusConflict},
		{errcode.ErrSagaDuplicateInstance, "ERR_SAGA_DUPLICATE_INSTANCE", errcode.KindConflict, http.StatusConflict},
	}
	for _, tc := range cases {
		if got := string(tc.code); got != tc.wantString {
			t.Errorf("code %v: got string %q, want %q", tc.code, got, tc.wantString)
		}
		err := errcode.New(tc.wantKind, tc.code, "saga journal: probe message")
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
