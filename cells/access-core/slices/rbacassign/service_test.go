package rbacassign

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func newTestService() (*Service, *mem.RoleRepository) {
	repo := mem.NewRoleRepository()
	repo.SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	return NewService(repo, slog.Default()), repo
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
				_ = r.AssignToUser(context.Background(), "usr-1", "admin")
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
			svc, repo := newTestService()
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
			name:   "revoke assigned role",
			userID: "usr-1",
			roleID: "admin",
			setup: func(r *mem.RoleRepository) {
				_ = r.AssignToUser(context.Background(), "usr-1", "admin")
			},
			wantErr: false,
		},
		{
			name:    "revoke unassigned role is idempotent",
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
			svc, repo := newTestService()
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
