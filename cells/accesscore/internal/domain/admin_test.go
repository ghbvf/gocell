package domain_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestNewLastAdminGuard_NilCounter_Rejected(t *testing.T) {
	t.Parallel()
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

func TestLastAdminGuard_CheckRemove(t *testing.T) {
	t.Parallel()
	sentinelInfra := errors.New("counter outage")

	tests := []struct {
		name         string
		hasAdminRole bool
		count        int
		countErr     error
		wantCode     errcode.Code
		wantErrIs    error
	}{
		{
			name:         "non_admin_short_circuits",
			hasAdminRole: false,
			count:        0, // counter would not even be called
			countErr:     sentinelInfra,
			wantCode:     "",
		},
		{
			name:         "admin_with_two_holders_allowed",
			hasAdminRole: true,
			count:        2,
		},
		{
			name:         "admin_sole_holder_rejected",
			hasAdminRole: true,
			count:        1,
			wantCode:     errcode.ErrAuthLastAdminProtected,
		},
		{
			name:         "admin_zero_count_rejected", // defense in depth: race / corruption
			hasAdminRole: true,
			count:        0,
			wantCode:     errcode.ErrAuthLastAdminProtected,
		},
		{
			name:         "admin_counter_error_propagated",
			hasAdminRole: true,
			countErr:     sentinelInfra,
			wantErrIs:    sentinelInfra,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			counterCalled := false
			guard, err := domain.NewLastAdminGuard(func(_ context.Context) (int, error) {
				counterCalled = true
				return tc.count, tc.countErr
			})
			if err != nil {
				t.Fatalf("NewLastAdminGuard: %v", err)
			}

			err = guard.CheckRemove(context.Background(), "user-123", tc.hasAdminRole)

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

			// non-admin path must NOT invoke the counter (avoid burning DB
			// queries when the answer is structurally "allowed").
			if !tc.hasAdminRole && counterCalled {
				t.Error("non-admin path must short-circuit before invoking counter")
			}
		})
	}
}
