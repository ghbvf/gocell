package rbaccheck

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// tenantCtx returns context.Background() with the canonical test tenant injected.
func tenantCtx() context.Context {
	return ctxkeys.WithTenantID(context.Background(), "00000000-0000-0000-0000-000000000001")
}

func newTestCodec(t *testing.T) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func newTestService(t *testing.T) (*Service, *mem.RoleRepository) {
	return newTestServiceWithMode(t, query.RunModeDemo)
}

func newTestServiceWithMode(t *testing.T, runMode query.RunMode) (*Service, *mem.RoleRepository) {
	t.Helper()
	repo := mem.NewStore(clock.Real()).RoleRepository()
	svc, err := NewService(repo, newTestCodec(t), slog.Default(), runMode,
		WithTxManager(outbox.DemoCellTxManager()))
	require.NoError(t, err)
	return svc, repo
}

func TestNewService_RequiresCodec(t *testing.T) {
	repo := mem.NewStore(clock.Real()).RoleRepository()
	svc, err := NewService(repo, nil, slog.Default(), query.RunModeProd)
	require.Error(t, err)
	require.Nil(t, svc)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
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
				r.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
				// SeedUserRoleAssignment bypasses F4 user-in-tenant check.
				r.SeedUserRoleAssignment(testTenantID, "usr-1", "admin")
			},
			userID: "usr-1", roleName: "admin", want: true,
		},
		{
			name: "does not have role",
			setup: func(r *mem.RoleRepository) {
				r.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
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

			has, err := svc.HasRole(tenantCtx(), tenant.SystemRowVisibility(), tt.userID, tt.roleName)
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
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	repo.SeedRole(testTenantID, &domain.Role{ID: "operator", Name: "operator"})
	repo.SeedRole(testTenantID, &domain.Role{ID: "viewer", Name: "viewer"})
	// SeedUserRoleAssignment bypasses F4 user-in-tenant check.
	repo.SeedUserRoleAssignment(testTenantID, "usr-1", "admin")
	repo.SeedUserRoleAssignment(testTenantID, "usr-1", "operator")
	repo.SeedUserRoleAssignment(testTenantID, "usr-1", "viewer")

	result, err := svc.ListRoles(tenantCtx(), tenant.SystemRowVisibility(), "usr-1", query.PageParams{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.True(t, result.HasMore)
	require.NotEmpty(t, result.NextCursor)

	next, err := svc.ListRoles(tenantCtx(), tenant.SystemRowVisibility(), "usr-1", query.PageParams{
		Limit:  2,
		Cursor: result.NextCursor,
	})
	require.NoError(t, err)
	assert.Len(t, next.Items, 1)
	assert.False(t, next.HasMore)
	assert.Empty(t, next.NextCursor)
}

func TestService_ListRolesEmptyInput(t *testing.T) {
	svc, _ := newTestService(t)
	_, err := svc.ListRoles(tenantCtx(), tenant.SystemRowVisibility(), "", query.PageParams{})
	assert.Error(t, err)
}

func TestService_ListRoles_ProdMode_BadCursor_ReturnsError(t *testing.T) {
	svc, repo := newTestServiceWithMode(t, query.RunModeProd)
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	repo.SeedUserRoleAssignment(testTenantID, "usr-1", "admin")

	_, err := svc.ListRoles(tenantCtx(), tenant.SystemRowVisibility(), "usr-1", query.PageParams{
		Limit:  50,
		Cursor: "not-a-valid-cursor",
	})
	require.Error(t, err)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}

// scopeCapturingRoleRepo wraps a RoleRepository and records whether the
// context passed to GetByUserID / ListByUserID carries a tenant scope.
// Used by TestHasRole_IsRLSScoped and TestListRoles_IsRLSScoped (Site 5).
type scopeCapturingRoleRepo struct {
	inner         ports.RoleRepository
	capturedScope tenant.TenantID
	capturedOK    bool
}

var _ ports.RoleRepository = (*scopeCapturingRoleRepo)(nil)

func (r *scopeCapturingRoleRepo) GetByID(ctx context.Context, t tenant.TenantID, id string) (*domain.Role, error) {
	return r.inner.GetByID(ctx, t, id)
}

func (r *scopeCapturingRoleRepo) GetByUserID(
	ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, userID string,
) ([]*domain.Role, error) {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.GetByUserID(ctx, t, vis, userID)
}

func (r *scopeCapturingRoleRepo) ListByUserID(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, userID string, params query.ListParams) ([]*domain.Role, error) { //nolint:lll // test fake stub signature; cannot be meaningfully split
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.ListByUserID(ctx, t, vis, userID, params)
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

// TestListRoles_IsRLSScoped asserts that ListRoles wraps the ListByUserID call
// in a scoped transaction so the RLS tenant_isolation policy on role_assignments
// is satisfied (Site 5 RLS fix, paired with TestHasRole_IsRLSScoped).
func TestListRoles_IsRLSScoped(t *testing.T) {
	inner := mem.NewStore(clock.Real()).RoleRepository()
	inner.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	inner.SeedUserRoleAssignment(testTenantID, "usr-rls-list", "admin")

	cap := &scopeCapturingRoleRepo{inner: inner}
	svc, err := NewService(cap, newTestCodec(t), slog.Default(), query.RunModeDemo,
		WithTxManager(outbox.DemoCellTxManager()))
	require.NoError(t, err)

	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	result, err := svc.ListRoles(ctx, tenant.SystemRowVisibility(), "usr-rls-list", query.PageParams{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, result.Items, 1)

	assert.True(t, cap.capturedOK,
		"ListByUserID must run inside a scoped tx (tenant.ScopeFromContext must be set)")
	assert.Equal(t, testTenantID, cap.capturedScope,
		"ListByUserID scope must equal the request tenant")
}

// TestHasRole_IsRLSScoped asserts that HasRole wraps the GetByUserID call in a
// scoped transaction so the RLS tenant_isolation policy on role_assignments is
// satisfied (Site 5 RLS fix).
//
// This is a RED test until Site 5 (rbaccheck scopedtx.Do) is implemented.
func TestHasRole_IsRLSScoped(t *testing.T) {
	inner := mem.NewStore(clock.Real()).RoleRepository()
	inner.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	inner.SeedUserRoleAssignment(testTenantID, "usr-rls", "admin")

	cap := &scopeCapturingRoleRepo{inner: inner}
	svc, err := NewService(cap, newTestCodec(t), slog.Default(), query.RunModeDemo,
		WithTxManager(outbox.DemoCellTxManager()))
	require.NoError(t, err)

	// Use a context that carries ctxkeys.TenantID (post-auth path).
	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	has, err := svc.HasRole(ctx, tenant.SystemRowVisibility(), "usr-rls", "admin")
	require.NoError(t, err)
	assert.True(t, has)

	assert.True(t, cap.capturedOK,
		"GetByUserID must run inside a scoped tx (tenant.ScopeFromContext must be set)")
	assert.Equal(t, testTenantID, cap.capturedScope,
		"GetByUserID scope must equal the request tenant")
}

// TestService_HasRole_RowScope_SelfMatch (#1709): service threads vis correctly —
// RowScopeSelf with subject==userID returns true when the role is held.
func TestService_HasRole_RowScope_SelfMatch(t *testing.T) {
	svc, repo := newTestService(t)
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	repo.SeedUserRoleAssignment(testTenantID, "usr-self-match", "admin")

	// Self obligation: subject == userID → owner predicate matches → role visible.
	vis, err := tenant.NewRowVisibility(tenant.RowScopeSelf, "usr-self-match")
	require.NoError(t, err)

	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	has, err := svc.HasRole(ctx, vis, "usr-self-match", "admin")
	require.NoError(t, err)
	assert.True(t, has, "RowScopeSelf matching own userID must find the seeded role")
}

// TestService_HasRole_RowScope_SelfMismatch_IDORCollapse (#1709): service threads
// vis correctly — RowScopeSelf with subject≠userID IDOR-collapses to
// hasRole=false (empty repo result for the mismatch subject).
func TestService_HasRole_RowScope_SelfMismatch_IDORCollapse(t *testing.T) {
	svc, repo := newTestService(t)
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	repo.SeedUserRoleAssignment(testTenantID, "usr-victim-role", "admin")

	// Self obligation: subject ≠ userID → IDOR collapse → empty roles → hasRole=false.
	vis, err := tenant.NewRowVisibility(tenant.RowScopeSelf, "attacker-subject")
	require.NoError(t, err)

	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	has, err := svc.HasRole(ctx, vis, "usr-victim-role", "admin")
	require.NoError(t, err)
	assert.False(t, has,
		"RowScopeSelf mismatch must IDOR-collapse: attacker must not see victim's role")
}

// TestService_ListRoles_RowScope_SelfMatch (#1709): service threads vis correctly —
// RowScopeSelf with subject==userID returns the user's roles.
func TestService_ListRoles_RowScope_SelfMatch(t *testing.T) {
	svc, repo := newTestService(t)
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	repo.SeedUserRoleAssignment(testTenantID, "usr-list-self-match", "admin")

	// Self obligation: subject == userID → owner predicate matches → roles returned.
	vis, err := tenant.NewRowVisibility(tenant.RowScopeSelf, "usr-list-self-match")
	require.NoError(t, err)

	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	result, err := svc.ListRoles(ctx, vis, "usr-list-self-match", query.PageParams{Limit: 10})
	require.NoError(t, err)
	assert.NotEmpty(t, result.Items, "RowScopeSelf matching own userID must return the seeded role")
}

// TestService_ListRoles_RowScope_SelfMismatch_IDORCollapse (#1709): service threads
// vis correctly — RowScopeSelf with subject≠userID IDOR-collapses to empty page.
func TestService_ListRoles_RowScope_SelfMismatch_IDORCollapse(t *testing.T) {
	svc, repo := newTestService(t)
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	repo.SeedUserRoleAssignment(testTenantID, "usr-list-victim", "admin")

	// Self obligation: subject ≠ userID → IDOR collapse → empty page.
	vis, err := tenant.NewRowVisibility(tenant.RowScopeSelf, "attacker-list-subject")
	require.NoError(t, err)

	ctx := ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
	result, err := svc.ListRoles(ctx, vis, "usr-list-victim", query.PageParams{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, result.Items,
		"RowScopeSelf mismatch must IDOR-collapse to empty page for ListRoles")
}
