package authorizationdecide

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// tenantCtx returns context.Background() with the canonical test tenant injected.
func tenantCtx() context.Context {
	return ctxkeys.WithTenantID(context.Background(), "00000000-0000-0000-0000-000000000001")
}

// testTenantID is the canonical test tenant UUID used in authorizationdecide tests.
var testTenantID = func() tenant.TenantID {
	t, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		panic("authorizationdecide_test: invalid testTenantID: " + err.Error())
	}
	return t
}()

// TestNewService_NilRoleRepo verifies that NewService rejects a nil roleRepo
// with a non-nil errcode.Error of KindInternal (wiring failure → 5xx).
func TestNewService_NilRoleRepo(t *testing.T) {
	tests := []struct {
		name     string
		roleRepo ports.RoleRepository
	}{
		{"bare nil", nil},
		// Real typed-nil: nil *mem.RoleRepository boxed into the interface —
		// non-nil interface, nil underlying. validation.IsNilInterface catches
		// it; bare `== nil` would not.
		{"typed nil", (*mem.RoleRepository)(nil)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(tt.roleRepo, slog.Default())
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.KindInternal, ecErr.Kind)
		})
	}
}

func newTestService() (*Service, *mem.RoleRepository) {
	repo := mem.NewStore(clock.Real()).RoleRepository()
	svc, err := NewService(repo, slog.Default())
	if err != nil {
		panic(err)
	}
	return svc, repo
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
				r.SeedRole(testTenantID, &domain.Role{
					ID: "admin", Name: "admin",
					Permissions: []domain.Permission{{Resource: "/api/v1/config", Action: "write"}},
				})
				_, _ = r.AssignToUser(context.Background(), testTenantID, "usr-1", "admin")
			},
			subject: "usr-1", resource: "/api/v1/config", action: "write",
			want: true,
		},
		{
			name: "unauthorized - no matching permission",
			setup: func(r *mem.RoleRepository) {
				r.SeedRole(testTenantID, &domain.Role{
					ID: "viewer", Name: "viewer",
					Permissions: []domain.Permission{{Resource: "/api/v1/config", Action: "read"}},
				})
				_, _ = r.AssignToUser(context.Background(), testTenantID, "usr-2", "viewer")
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

			allowed, err := svc.Authorize(tenantCtx(), tt.subject, tt.resource, tt.action)
			require.NoError(t, err)
			assert.Equal(t, tt.want, allowed)
		})
	}
}
