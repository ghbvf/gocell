package credentialauthority_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// activeUser builds an active *domain.User with the given PasswordVersion.
func activeUser(t *testing.T, pwVersion int64) *domain.User {
	t.Helper()
	return rebuild(t, domain.StatusActive, pwVersion)
}

func lockedUser(t *testing.T) *domain.User {
	t.Helper()
	return rebuild(t, domain.StatusLocked, 1)
}

func suspendedUser(t *testing.T) *domain.User {
	t.Helper()
	return rebuild(t, domain.StatusSuspended, 1)
}

func rebuild(t *testing.T, status domain.UserStatus, pwVersion int64) *domain.User {
	t.Helper()
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:              "u1",
		Username:        "alice",
		Email:           "alice@example.com",
		PasswordHash:    "$2a$12$xxx",
		PasswordVersion: pwVersion,
		Source:          domain.UserSourceIdentity,
		CreatedAt:       time.Now().UTC(),
		UpdatedAt:       time.Now().UTC(),
		Status:          status,
		AuthzEpoch:      1,
	})
	if err != nil {
		t.Fatalf("ReconstituteUser: %v", err)
	}
	return u
}

func TestAssert(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	tests := []struct {
		name      string
		user      func(t *testing.T) *domain.User
		checks    func() []credentialauthority.Check
		wantKind  errcode.Kind
		wantCode  errcode.Code
		wantNil   bool
	}{
		{
			name:    "baseline_pass_active_user",
			user:    func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks:  func() []credentialauthority.Check { return nil },
			wantNil: true,
		},
		{
			name:     "baseline_fail_locked_user",
			user:     lockedUser,
			checks:   func() []credentialauthority.Check { return nil },
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
		{
			name:     "baseline_fail_suspended_user",
			user:     suspendedUser,
			checks:   func() []credentialauthority.Check { return nil },
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
		{
			name: "pin_pass_matching_version",
			user: func(t *testing.T) *domain.User { return activeUser(t, 7) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SnapshotPasswordVersion(activeUser(t, 7))}
			},
			wantNil: true,
		},
		{
			name: "pin_fail_stale_version",
			user: func(t *testing.T) *domain.User { return activeUser(t, 8) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SnapshotPasswordVersion(activeUser(t, 7))}
			},
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
		{
			name: "not_revoked_pass_nil",
			user: func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SessionNotRevoked(&session.ValidateView{ID: "s1", SubjectID: "u1", RevokedAt: nil, AuthzEpochAtIssue: 1})}
			},
			wantNil: true,
		},
		{
			name: "not_revoked_fail_nonnil",
			user: func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks: func() []credentialauthority.Check {
				ts := now
				return []credentialauthority.Check{credentialauthority.SessionNotRevoked(&session.ValidateView{ID: "s1", SubjectID: "u1", RevokedAt: &ts, AuthzEpochAtIssue: 1})}
			},
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
		{
			name: "compose_issue_path_baseline_plus_pin",
			user: func(t *testing.T) *domain.User { return activeUser(t, 3) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SnapshotPasswordVersion(activeUser(t, 3))}
			},
			wantNil: true,
		},
		{
			name: "compose_validate_path_baseline_plus_not_revoked",
			user: func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SessionNotRevoked(&session.ValidateView{ID: "s1", SubjectID: "u1", RevokedAt: nil, AuthzEpochAtIssue: 1})}
			},
			wantNil: true,
		},
		{
			name: "not_revoked_nil_view_fails_closed",
			user: func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SessionNotRevoked(nil)}
			},
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
		{
			name: "snapshot_password_version_nil_user_fails",
			user: func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SnapshotPasswordVersion(nil)}
			},
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
		{
			name:     "nil_user_returns_KindInvalid",
			user:     func(t *testing.T) *domain.User { return nil },
			checks:   func() []credentialauthority.Check { return nil },
			wantKind: errcode.KindInvalid,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name: "nil_check_in_variadic_returns_KindInvalid",
			user: func(t *testing.T) *domain.User { return activeUser(t, 1) },
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{nil}
			},
			wantKind: errcode.KindInvalid,
			wantCode: errcode.ErrValidationFailed,
		},
		{
			name: "baseline_failure_short_circuits_subsequent_checks",
			// locked user with a Check whose evaluation would otherwise pass —
			// baseline must reject before the Check.apply runs (order: baseline
			// before variadic). Asserts code is baseline reason, not check reason.
			user: lockedUser,
			checks: func() []credentialauthority.Check {
				return []credentialauthority.Check{credentialauthority.SnapshotPasswordVersion(activeUser(t, 1))}
			},
			wantKind: errcode.KindPermissionDenied,
			wantCode: errcode.ErrAuthUserNotActive,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			user := tc.user(t)
			err := credentialauthority.Assert(user, tc.checks()...)
			if tc.wantNil {
				if err != nil {
					t.Fatalf("Assert returned error %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Assert returned nil, want error")
			}
			var ec *errcode.Error
			if !errors.As(err, &ec) {
				t.Fatalf("error is not *errcode.Error: %T", err)
			}
			if ec.Kind != tc.wantKind {
				t.Errorf("kind = %v, want %v", ec.Kind, tc.wantKind)
			}
			if ec.Code != tc.wantCode {
				t.Errorf("code = %v, want %v", ec.Code, tc.wantCode)
			}
		})
	}
}
