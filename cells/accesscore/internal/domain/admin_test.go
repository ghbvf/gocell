package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// stubEffectiveAdminCounter is a test-only EffectiveAdminCounter that returns
// a fixed (count, err) pair and records invocation.
type stubEffectiveAdminCounter struct {
	count  int
	err    error
	called bool
}

func (s *stubEffectiveAdminCounter) CountEffectiveAdmins(_ context.Context) (int, error) {
	s.called = true
	return s.count, s.err
}

func TestNewLastAdminGuard_NilCounter_Rejected(t *testing.T) {
	t.Parallel()
	// Bare-nil interface value: pkg/validation.IsNilInterface rejects this and
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

func TestNewLastAdminGuard_TypedNilCounter_Rejected(t *testing.T) {
	t.Parallel()
	// Typed-nil also rejected via validation.IsNilInterface; matches the
	// kernel/runtime single-source typed-nil convention.
	var counter *stubEffectiveAdminCounter // typed-nil
	guard, err := domain.NewLastAdminGuard(counter)
	if err == nil || guard != nil {
		t.Fatalf("expected typed-nil to fail; got guard=%v err=%v", guard, err)
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
			stub := &stubEffectiveAdminCounter{count: tc.count, err: tc.countErr}
			guard, err := domain.NewLastAdminGuard(stub)
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
