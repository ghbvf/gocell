package rbacassign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func newTestService() (*Service, *mem.RoleRepository, *mem.SessionRepository) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	sessionRepo := mem.NewSessionRepository()
	return NewService(roleRepo, sessionRepo, slog.Default()), roleRepo, sessionRepo
}

func TestService_Assign(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mem.RoleRepository)
		userID   string
		roleID   string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:    "assign role to user",
			userID:  "usr-1",
			roleID:  "admin",
			wantErr: false,
		},
		{
			name:   "assign same role twice is idempotent",
			userID: "usr-1",
			roleID: "admin",
			setup: func(r *mem.RoleRepository) {
				_, _ = r.AssignToUser(context.Background(), "usr-1", "admin")
			},
			wantErr: false,
		},
		{
			name:     "empty userId returns error",
			userID:   "",
			roleID:   "admin",
			wantErr:  true,
			wantCode: errcode.ErrAuthRBACInvalidInput,
		},
		{
			name:     "empty roleId returns error",
			userID:   "usr-1",
			roleID:   "",
			wantErr:  true,
			wantCode: errcode.ErrAuthRBACInvalidInput,
		},
		{
			name:     "role not found returns error",
			userID:   "usr-1",
			roleID:   "nonexistent",
			wantErr:  true,
			wantCode: errcode.ErrAuthRoleNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := newTestService()
			if tc.setup != nil {
				tc.setup(repo)
			}

			err := svc.Assign(context.Background(), tc.userID, tc.roleID)
			if !tc.wantErr {
				require.NoError(t, err)
				// Verify assignment persisted.
				roles, _ := repo.GetByUserID(context.Background(), tc.userID)
				var found bool
				for _, r := range roles {
					if r.ID == tc.roleID {
						found = true
					}
				}
				assert.True(t, found, "role %s should be assigned to user %s", tc.roleID, tc.userID)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

func TestService_Revoke(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mem.RoleRepository)
		userID   string
		roleID   string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:   "revoke assigned role with multiple holders",
			userID: "usr-1",
			roleID: "admin",
			setup: func(r *mem.RoleRepository) {
				_, _ = r.AssignToUser(context.Background(), "usr-1", "admin")
				_, _ = r.AssignToUser(context.Background(), "usr-2", "admin")
			},
			wantErr: false,
		},
		{
			name:   "revoke last admin returns error",
			userID: "usr-1",
			roleID: "admin",
			setup: func(r *mem.RoleRepository) {
				_, _ = r.AssignToUser(context.Background(), "usr-1", "admin")
			},
			wantErr:  true,
			wantCode: errcode.ErrAuthForbidden,
		},
		{
			name:    "revoke unassigned role with no holders is guarded",
			userID:  "usr-1",
			roleID:  "admin",
			wantErr: false,
		},
		{
			name:     "empty userId returns error",
			userID:   "",
			roleID:   "admin",
			wantErr:  true,
			wantCode: errcode.ErrAuthRBACInvalidInput,
		},
		{
			name:     "empty roleId returns error",
			userID:   "usr-1",
			roleID:   "",
			wantErr:  true,
			wantCode: errcode.ErrAuthRBACInvalidInput,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo, _ := newTestService()
			if tc.setup != nil {
				tc.setup(repo)
			}

			err := svc.Revoke(context.Background(), tc.userID, tc.roleID)
			if !tc.wantErr {
				require.NoError(t, err)
				// Verify removal persisted.
				roles, _ := repo.GetByUserID(context.Background(), tc.userID)
				for _, r := range roles {
					assert.NotEqual(t, tc.roleID, r.ID, "role %s should not be assigned to user %s after revoke", tc.roleID, tc.userID)
				}
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tc.wantCode, ecErr.Code)
		})
	}
}

func TestService_Revoke_InvalidatesSessions(t *testing.T) {
	svc, roleRepo, sessionRepo := newTestService()
	ctx := context.Background()

	_, _ = roleRepo.AssignToUser(ctx, "usr-1", "admin")
	_, _ = roleRepo.AssignToUser(ctx, "usr-2", "admin") // second admin to pass last-admin guard
	sess := &domain.Session{ID: "sess-1", UserID: "usr-1"}
	require.NoError(t, sessionRepo.Create(ctx, sess))

	require.NoError(t, svc.Revoke(ctx, "usr-1", "admin"))

	s, err := sessionRepo.GetByID(ctx, "sess-1")
	require.NoError(t, err)
	assert.True(t, s.IsRevoked(), "session must be revoked after role change")
}

func TestService_Assign_InvalidatesSessions(t *testing.T) {
	svc, _, sessionRepo := newTestService()
	ctx := context.Background()

	sess := &domain.Session{ID: "sess-2", UserID: "usr-2"}
	require.NoError(t, sessionRepo.Create(ctx, sess))

	require.NoError(t, svc.Assign(ctx, "usr-2", "admin"))

	s, err := sessionRepo.GetByID(ctx, "sess-2")
	require.NoError(t, err)
	assert.True(t, s.IsRevoked(), "session must be revoked after role assignment")
}

// failingSessionRepo returns an error on RevokeByUserID to test fail-closed behavior.
type failingSessionRepo struct{ ports.SessionRepository }

func (failingSessionRepo) RevokeByUserID(_ context.Context, _ string) error {
	return fmt.Errorf("session store unavailable")
}

func TestService_Revoke_SessionRevokeFail_ReturnsError(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	_, _ = roleRepo.AssignToUser(context.Background(), "usr-1", "admin")
	_, _ = roleRepo.AssignToUser(context.Background(), "usr-2", "admin") // second admin to pass last-admin guard

	svc := NewService(roleRepo, failingSessionRepo{}, slog.Default())
	err := svc.Revoke(context.Background(), "usr-1", "admin")
	require.Error(t, err, "revoke must fail-closed when session revocation fails")
	assert.Contains(t, err.Error(), "session revoke failed")
}

func TestService_Assign_SessionRevokeFail_ReturnsError(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	svc := NewService(roleRepo, failingSessionRepo{}, slog.Default())
	err := svc.Assign(context.Background(), "usr-1", "admin")
	require.Error(t, err, "assign must fail-closed when session revocation fails")
	assert.Contains(t, err.Error(), "session revoke failed")
}

// TestService_DemoMode_* proves that in demo mode (no WithOutboxWriter / WithTxManager),
// sessionRepo.RevokeByUserID is still called exactly once per Assign and Revoke, preserving
// backward-compatible dual-write behaviour.
func TestService_DemoMode_Assign_CallsSessionRevoke(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		roleID string
	}{
		{name: "demo assign calls session revoke", userID: "usr-demo", roleID: "admin"},
		{name: "demo assign second user also calls session revoke", userID: "usr-demo-2", roleID: "admin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			roleRepo := mem.NewRoleRepository()
			roleRepo.SeedRole(&domain.Role{
				ID:          "admin",
				Name:        "admin",
				Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
			})
			sessionRepo := mem.NewSessionRepository()
			// Create a session for the user so we can verify revocation.
			sess := &domain.Session{ID: "sess-" + tc.userID, UserID: tc.userID}
			require.NoError(t, sessionRepo.Create(context.Background(), sess))

			// No opts → demo mode.
			svc := NewService(roleRepo, sessionRepo, slog.Default())
			require.NoError(t, svc.Assign(context.Background(), tc.userID, tc.roleID))

			s, err := sessionRepo.GetByID(context.Background(), "sess-"+tc.userID)
			require.NoError(t, err)
			assert.True(t, s.IsRevoked(), "demo mode: session must be revoked after Assign")
		})
	}
}

func TestService_DemoMode_Revoke_CallsSessionRevoke(t *testing.T) {
	tests := []struct {
		name   string
		userID string
		roleID string
	}{
		{name: "demo revoke calls session revoke", userID: "usr-demo-r", roleID: "admin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			roleRepo := mem.NewRoleRepository()
			roleRepo.SeedRole(&domain.Role{
				ID:          "admin",
				Name:        "admin",
				Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
			})
			_, _ = roleRepo.AssignToUser(context.Background(), tc.userID, "admin")
			_, _ = roleRepo.AssignToUser(context.Background(), "usr-other", "admin") // second admin
			sessionRepo := mem.NewSessionRepository()
			sess := &domain.Session{ID: "sess-" + tc.userID, UserID: tc.userID}
			require.NoError(t, sessionRepo.Create(context.Background(), sess))

			svc := NewService(roleRepo, sessionRepo, slog.Default())
			require.NoError(t, svc.Revoke(context.Background(), tc.userID, tc.roleID))

			s, err := sessionRepo.GetByID(context.Background(), "sess-"+tc.userID)
			require.NoError(t, err)
			assert.True(t, s.IsRevoked(), "demo mode: session must be revoked after Revoke")
		})
	}
}
