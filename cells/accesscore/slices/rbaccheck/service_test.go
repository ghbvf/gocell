package rbaccheck

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
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
	svc, err := NewService(repo, newTestCodec(t), slog.Default(), runMode)
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
				_, _ = r.AssignToUser(context.Background(), testTenantID, "usr-1", "admin")
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

			has, err := svc.HasRole(tenantCtx(), tt.userID, tt.roleName)
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
	_, _ = repo.AssignToUser(context.Background(), testTenantID, "usr-1", "admin")
	_, _ = repo.AssignToUser(context.Background(), testTenantID, "usr-1", "operator")
	_, _ = repo.AssignToUser(context.Background(), testTenantID, "usr-1", "viewer")

	result, err := svc.ListRoles(tenantCtx(), "usr-1", query.PageParams{Limit: 2})
	require.NoError(t, err)
	assert.Len(t, result.Items, 2)
	assert.True(t, result.HasMore)
	require.NotEmpty(t, result.NextCursor)

	next, err := svc.ListRoles(tenantCtx(), "usr-1", query.PageParams{
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
	_, err := svc.ListRoles(tenantCtx(), "", query.PageParams{})
	assert.Error(t, err)
}

func TestService_ListRoles_ProdMode_BadCursor_ReturnsError(t *testing.T) {
	svc, repo := newTestServiceWithMode(t, query.RunModeProd)
	repo.SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	_, _ = repo.AssignToUser(context.Background(), testTenantID, "usr-1", "admin")

	_, err := svc.ListRoles(tenantCtx(), "usr-1", query.PageParams{
		Limit:  50,
		Cursor: "not-a-valid-cursor",
	})
	require.Error(t, err)

	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
}
