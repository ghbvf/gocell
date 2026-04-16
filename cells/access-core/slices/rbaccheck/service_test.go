package rbaccheck

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestService() (*Service, *mem.RoleRepository) {
	repo := mem.NewRoleRepository()
	return NewService(repo, slog.Default()), repo
}

func TestService_HasRole(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mem.RoleRepository)
		userID   string
		roleName string
		want     bool
		wantErr  bool
	}{
		{
			name: "has role",
			setup: func(r *mem.RoleRepository) {
				r.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
				_ = r.AssignToUser(context.Background(), "usr-1", "admin")
			},
			userID: "usr-1", roleName: "admin", want: true,
		},
		{
			name: "does not have role",
			setup: func(r *mem.RoleRepository) {
				r.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
			},
			userID: "usr-2", roleName: "admin", want: false,
		},
		{
			name:   "empty input",
			setup:  func(_ *mem.RoleRepository) {},
			userID: "", roleName: "admin",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			tt.setup(repo)

			has, err := svc.HasRole(context.Background(), tt.userID, tt.roleName)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, has)
			}
		})
	}
}

func TestService_ListRoles(t *testing.T) {
	svc, repo := newTestService()
	repo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	repo.SeedRole(&domain.Role{ID: "viewer", Name: "viewer"})
	_ = repo.AssignToUser(context.Background(), "usr-1", "admin")
	_ = repo.AssignToUser(context.Background(), "usr-1", "viewer")

	roles, err := svc.ListRoles(context.Background(), "usr-1")
	require.NoError(t, err)
	assert.Len(t, roles, 2)
}

func TestService_ListRolesEmptyInput(t *testing.T) {
	svc, _ := newTestService()
	_, err := svc.ListRoles(context.Background(), "")
	assert.Error(t, err)
}
