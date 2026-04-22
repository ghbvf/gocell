package rbaccheck

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCodec(t *testing.T) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func newTestService(t *testing.T) (*Service, *mem.RoleRepository) {
	t.Helper()
	repo := mem.NewRoleRepository()
	return NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo), repo
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
				_, _ = r.AssignToUser(context.Background(), "usr-1", "admin")
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
			svc, repo := newTestService(t)
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
	svc, repo := newTestService(t)
	repo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	repo.SeedRole(&domain.Role{ID: "viewer", Name: "viewer"})
	_, _ = repo.AssignToUser(context.Background(), "usr-1", "admin")
	_, _ = repo.AssignToUser(context.Background(), "usr-1", "viewer")

	result, err := svc.ListRoles(context.Background(), "usr-1", query.PageRequest{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
}

func TestService_ListRolesEmptyInput(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.ListRoles(context.Background(), "", query.PageRequest{})
	assert.Error(t, err)
}
