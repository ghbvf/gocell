package authorizationdecide

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

func TestService_Authorize(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*mem.RoleRepository)
		subject  string
		resource string
		action   string
		want     bool
	}{
		{
			name: "authorized via role permission",
			setup: func(r *mem.RoleRepository) {
				r.SeedRole(&domain.Role{
					ID: "admin", Name: "admin",
					Permissions: []domain.Permission{{Resource: "/api/v1/config", Action: "write"}},
				})
				_ = r.AssignToUser(context.Background(), "usr-1", "admin")
			},
			subject: "usr-1", resource: "/api/v1/config", action: "write",
			want: true,
		},
		{
			name: "unauthorized - no matching permission",
			setup: func(r *mem.RoleRepository) {
				r.SeedRole(&domain.Role{
					ID: "viewer", Name: "viewer",
					Permissions: []domain.Permission{{Resource: "/api/v1/config", Action: "read"}},
				})
				_ = r.AssignToUser(context.Background(), "usr-2", "viewer")
			},
			subject: "usr-2", resource: "/api/v1/config", action: "write",
			want: false,
		},
		{
			name:    "unauthorized - no roles",
			setup:   func(_ *mem.RoleRepository) {},
			subject: "usr-3", resource: "/api/v1/config", action: "read",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			tt.setup(repo)

			allowed, err := svc.Authorize(context.Background(), tt.subject, tt.resource, tt.action)
			require.NoError(t, err)
			assert.Equal(t, tt.want, allowed)
		})
	}
}
