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
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
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

// testTenantIDStr is the string form for use with ctxkeys.WithTenantID.
const testTenantIDStr = "00000000-0000-0000-0000-000000000001"

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
	svc, err := NewService(repo, slog.Default(),
		WithTxManager(outbox.DemoCellTxManager()))
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
				// SeedUserRoleAssignment bypasses the F4 user-in-tenant check —
				// this test exercises authorization decision, not user-exists guard.
				r.SeedUserRoleAssignment(testTenantID, "usr-1", "admin")
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
				r.SeedUserRoleAssignment(testTenantID, "usr-2", "viewer")
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

// scopeCapturingRoleRepo wraps a RoleRepository and records whether the
// context passed to GetByUserID carries a tenant scope.
// Used by TestAuthorize_IsRLSScoped (Site 6).
type scopeCapturingRoleRepo struct {
	inner         ports.RoleRepository
	capturedScope tenant.TenantID
	capturedOK    bool
}

var _ ports.RoleRepository = (*scopeCapturingRoleRepo)(nil)

func (r *scopeCapturingRoleRepo) GetByID(ctx context.Context, t tenant.TenantID, id string) (*domain.Role, error) {
	return r.inner.GetByID(ctx, t, id)
}

func (r *scopeCapturingRoleRepo) GetByUserID(ctx context.Context, t tenant.TenantID, userID string) ([]*domain.Role, error) {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.GetByUserID(ctx, t, userID)
}

func (r *scopeCapturingRoleRepo) ListByUserID(ctx context.Context, t tenant.TenantID, userID string, params query.ListParams) ([]*domain.Role, error) {
	return r.inner.ListByUserID(ctx, t, userID, params)
}

func (r *scopeCapturingRoleRepo) Create(ctx context.Context, t tenant.TenantID, role *domain.Role) error {
	return r.inner.Create(ctx, t, role)
}

func (r *scopeCapturingRoleRepo) AssignToUser(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	return r.inner.AssignToUser(ctx, t, userID, roleID)
}

func (r *scopeCapturingRoleRepo) RemoveFromUser(ctx context.Context, t tenant.TenantID, userID, roleID string) error {
	return r.inner.RemoveFromUser(ctx, t, userID, roleID)
}

func (r *scopeCapturingRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	return r.inner.RemoveFromUserIfNotLast(ctx, t, userID, roleID)
}

func (r *scopeCapturingRoleRepo) CountByRole(ctx context.Context, t tenant.TenantID, roleID string) (int, error) {
	return r.inner.CountByRole(ctx, t, roleID)
}

func (r *scopeCapturingRoleRepo) CountEffectiveAdmins(ctx context.Context, t tenant.TenantID) (int, error) {
	return r.inner.CountEffectiveAdmins(ctx, t)
}

func (r *scopeCapturingRoleRepo) EffectiveAdminExists(ctx context.Context, t tenant.TenantID) (bool, error) {
	return r.inner.EffectiveAdminExists(ctx, t)
}

// TestAuthorize_IsRLSScoped asserts that Authorize wraps the GetByUserID call
// in a scoped transaction so the RLS tenant_isolation policy on role_assignments
// is satisfied (Site 6 RLS fix).
//
// This is a RED test until Site 6 (authorizationdecide scopedtx.Do) is
// implemented.
func TestAuthorize_IsRLSScoped(t *testing.T) {
	inner := mem.NewStore(clock.Real()).RoleRepository()
	inner.SeedRole(testTenantID, &domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{{Resource: "/api/v1/config", Action: "write"}},
	})
	inner.SeedUserRoleAssignment(testTenantID, "usr-rls", "admin")

	cap := &scopeCapturingRoleRepo{inner: inner}
	svc, err := NewService(cap, slog.Default(),
		WithTxManager(outbox.DemoCellTxManager()))
	require.NoError(t, err)

	// Use a context that carries ctxkeys.TenantID (post-auth path).
	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	allowed, err := svc.Authorize(ctx, "usr-rls", "/api/v1/config", "write")
	require.NoError(t, err)
	assert.True(t, allowed)

	assert.True(t, cap.capturedOK,
		"GetByUserID must run inside a scoped tx (tenant.ScopeFromContext must be set)")
	assert.Equal(t, testTenantID, cap.capturedScope,
		"GetByUserID scope must equal the request tenant")
}
