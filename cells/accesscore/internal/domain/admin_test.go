package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// stubEffectiveAdminCounterImpl is a test-only EffectiveAdminCounterImpl
// that returns a fixed (count, err) pair and records invocation. The
// sealed EffectiveAdminCounter is obtained by wrapping this stub via
// domain.WrapEffectiveAdminCounter (the only construction path).
type stubEffectiveAdminCounterImpl struct {
	count  int
	err    error
	called bool
}

func (s *stubEffectiveAdminCounterImpl) CountEffectiveAdmins(_ context.Context) (int, error) {
	s.called = true
	return s.count, s.err
}

func TestNewLastAdminGuard_NilCounter_Rejected(t *testing.T) {
	t.Parallel()
	// Bare-nil sealed wrapper: pkg/validation.IsNilInterface rejects this and
	// NewLastAdminGuard returns ErrValidationFailed.
	guard, err := domain.NewLastAdminGuard(nil)
	if err == nil {
		t.Fatal("expected error for nil counter")
	}
	if guard != nil {
		t.Fatalf("expected nil guard on error, got %+v", guard)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) || coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %v", err)
	}
}

func TestWrapEffectiveAdminCounter_NilImpl_Rejected(t *testing.T) {
	t.Parallel()
	// Bare-nil EffectiveAdminCounterImpl rejected at the wrap boundary so
	// NewLastAdminGuard never sees a typed-nil sealed value.
	sealed, err := domain.WrapEffectiveAdminCounter(nil)
	if err == nil || sealed != nil {
		t.Fatalf("expected bare-nil to fail; got sealed=%v err=%v", sealed, err)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) || coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %v", err)
	}
}

func TestWrapEffectiveAdminCounter_TypedNilImpl_Rejected(t *testing.T) {
	t.Parallel()
	// Typed-nil also rejected via validation.IsNilInterface at the wrap
	// boundary; matches the kernel/runtime single-source typed-nil convention
	// and matches kernel/persistence.WrapForCell behavior.
	var impl *stubEffectiveAdminCounterImpl // typed-nil
	sealed, err := domain.WrapEffectiveAdminCounter(impl)
	if err == nil || sealed != nil {
		t.Fatalf("expected typed-nil impl to fail; got sealed=%v err=%v", sealed, err)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) || coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %v", err)
	}
}

func TestLastAdminGuard_CheckRemove(t *testing.T) {
	t.Parallel()
	sentinelInfra := errors.New("counter outage")

	tests := []struct {
		name              string
		userIsActiveAdmin bool
		count             int
		countErr          error
		wantCode          errcode.Code
		wantErrIs         error
	}{
		{
			name:              "non_effective_admin_short_circuits",
			userIsActiveAdmin: false,
			count:             0, // counter would not even be called
			countErr:          sentinelInfra,
			wantCode:          "",
		},
		{
			name:              "active_admin_with_another_active_admin_allowed",
			userIsActiveAdmin: true,
			count:             2,
		},
		{
			name:              "sole_effective_admin_rejected",
			userIsActiveAdmin: true,
			count:             1,
			wantCode:          errcode.ErrAuthLastAdminProtected,
		},
		{
			name:              "zero_count_rejected", // defense in depth: race / corruption
			userIsActiveAdmin: true,
			count:             0,
			wantCode:          errcode.ErrAuthLastAdminProtected,
		},
		{
			name:              "counter_error_propagated",
			userIsActiveAdmin: true,
			countErr:          sentinelInfra,
			wantErrIs:         sentinelInfra,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stub := &stubEffectiveAdminCounterImpl{count: tc.count, err: tc.countErr}
			sealed, wrapErr := domain.WrapEffectiveAdminCounter(stub)
			if wrapErr != nil {
				t.Fatalf("WrapEffectiveAdminCounter: %v", wrapErr)
			}
			guard, err := domain.NewLastAdminGuard(sealed)
			if err != nil {
				t.Fatalf("NewLastAdminGuard: %v", err)
			}

			err = guard.CheckRemove(context.Background(), "user-123", tc.userIsActiveAdmin)

			switch {
			case tc.wantErrIs != nil:
				if !errors.Is(err, tc.wantErrIs) {
					t.Fatalf("expected error wrapping %v, got %v", tc.wantErrIs, err)
				}
			case tc.wantCode != "":
				var coded *errcode.Error
				if !errors.As(err, &coded) {
					t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
				}
				if coded.Code != tc.wantCode {
					t.Errorf("code: got %s, want %s", coded.Code, tc.wantCode)
				}
				if coded.Kind != errcode.KindPermissionDenied {
					t.Errorf("kind: got %v, want KindPermissionDenied", coded.Kind)
				}
			default:
				if err != nil {
					t.Errorf("expected nil error, got %v", err)
				}
			}

			// non-effective-admin path must NOT invoke the counter (avoid burning DB
			// queries when the answer is structurally "allowed").
			if !tc.userIsActiveAdmin && stub.called {
				t.Error("non-effective-admin path must short-circuit before invoking counter")
			}
		})
	}
}
