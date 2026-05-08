package rbacassign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// rbacFakeTxRunner is a test-only pass-through TxRunner (no real transaction).
type rbacFakeTxRunner struct{}

func (rbacFakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = rbacFakeTxRunner{}

type permissiveUserRepo struct{ ports.UserRepository }

func (permissiveUserRepo) GetByIDForUpdate(_ context.Context, id string) (*domain.User, error) {
	u, err := domain.NewUser("locked-"+id, id+"@test.local", "hash", time.Now())
	if err != nil {
		return nil, err
	}
	u.ID = id
	return u, nil
}

type trackingRefreshStore struct {
	refresh.Store
	revokeUserCalls int
	revokeUserIDs   []string
	err             error
}

func (s *trackingRefreshStore) RevokeUser(_ context.Context, userID string) error {
	s.revokeUserCalls++
	s.revokeUserIDs = append(s.revokeUserIDs, userID)
	return s.err
}

// mustNewService creates a Service with a fake TxRunner, failing the test on error.
func mustNewService(
	t testing.TB,
	roleRepo ports.RoleRepository,
	sessionRepo ports.SessionRepository,
	refreshStore refresh.Store,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	t.Helper()
	opts = append([]Option{WithTxManager(rbacFakeTxRunner{})}, opts...)
	svc, err := NewService(permissiveUserRepo{}, roleRepo, sessionRepo, refreshStore, logger, opts...)
	require.NoError(t, err)
	return svc
}

func newTestService(t testing.TB) (*Service, *mem.RoleRepository, ports.SessionRepository, *trackingRefreshStore) {
	t.Helper()
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := &trackingRefreshStore{}
	return mustNewService(t, roleRepo, sessionRepo, refreshStore, slog.Default()), roleRepo, sessionRepo, refreshStore
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	_, err := NewService(permissiveUserRepo{}, roleRepo, sessionRepo, &trackingRefreshStore{}, slog.Default() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

func TestNewService_RejectsNilDependencies(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := &trackingRefreshStore{}

	tests := []struct {
		name      string
		wantError string
		run       func() error
	}{
		{
			name:      "user repo",
			wantError: "userRepo",
			run: func() error {
				_, err := NewService(nil, roleRepo, sessionRepo, refreshStore, slog.Default())
				return err
			},
		},
		{
			name:      "role repo",
			wantError: "roleRepo",
			run: func() error {
				_, err := NewService(permissiveUserRepo{}, nil, sessionRepo, refreshStore, slog.Default())
				return err
			},
		},
		{
			name:      "session repo",
			wantError: "sessionRepo",
			run: func() error {
				_, err := NewService(permissiveUserRepo{}, roleRepo, nil, refreshStore, slog.Default())
				return err
			},
		},
		{
			name:      "refresh store",
			wantError: "refreshStore",
			run: func() error {
				_, err := NewService(permissiveUserRepo{}, roleRepo, sessionRepo, nil, slog.Default())
				return err
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
			assert.Contains(t, err.Error(), tc.wantError)
		})
	}
}

func TestNewService_DefaultsNilLogger(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	svc, err := NewService(
		permissiveUserRepo{},
		roleRepo,
		sessionRepo,
		&trackingRefreshStore{},
		nil,
		WithTxManager(rbacFakeTxRunner{}),
	)
	require.NoError(t, err)
	assert.NotNil(t, svc.logger)
}

type failingLockUserRepo struct{ ports.UserRepository }

func (failingLockUserRepo) GetByIDForUpdate(_ context.Context, _ string) (*domain.User, error) {
	return nil, errors.New("user row lock failed")
}

func TestService_Assign_LockUserFail_ReturnsError(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	sessionRepo := testutil.RealSessionRepo(t)
	svc, err := NewService(
		failingLockUserRepo{},
		roleRepo,
		sessionRepo,
		&trackingRefreshStore{},
		slog.Default(),
		WithTxManager(rbacFakeTxRunner{}),
	)
	require.NoError(t, err)

	err = svc.Assign(context.Background(), "usr-1", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lock user")
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
			svc, repo, _, _ := newTestService(t)
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
			svc, repo, _, _ := newTestService(t)
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
	svc, roleRepo, sessionRepo, refreshStore := newTestService(t)
	ctx := context.Background()

	_, _ = roleRepo.AssignToUser(ctx, "usr-1", "admin")
	_, _ = roleRepo.AssignToUser(ctx, "usr-2", "admin") // second admin to pass last-admin guard
	sess := &domain.Session{ID: "sess-1", UserID: "usr-1"}
	require.NoError(t, sessionRepo.Create(ctx, sess))

	require.NoError(t, svc.Revoke(ctx, "usr-1", "admin"))

	// After P2b soft-revoke: revoked sessions are invisible via GetByID.
	_, revokeErr := sessionRepo.GetByID(ctx, "sess-1")
	require.Error(t, revokeErr, "session must be invisible after soft-revoke")
	var ec *errcode.Error
	require.ErrorAs(t, revokeErr, &ec)
	assert.Equal(t, errcode.ErrSessionNotFound, ec.Code, "session must return ErrSessionNotFound")
	assert.Equal(t, 1, refreshStore.revokeUserCalls, "refresh chains must be revoked with role changes")
	assert.Equal(t, []string{"usr-1"}, refreshStore.revokeUserIDs)
}

func TestService_Assign_InvalidatesSessions(t *testing.T) {
	svc, _, sessionRepo, refreshStore := newTestService(t)
	ctx := context.Background()

	sess := &domain.Session{ID: "sess-2", UserID: "usr-2"}
	require.NoError(t, sessionRepo.Create(ctx, sess))

	require.NoError(t, svc.Assign(ctx, "usr-2", "admin"))

	// After P2b soft-revoke: revoked sessions are invisible via GetByID.
	_, revokeErr2 := sessionRepo.GetByID(ctx, "sess-2")
	require.Error(t, revokeErr2, "session must be invisible after soft-revoke")
	var ec2 *errcode.Error
	require.ErrorAs(t, revokeErr2, &ec2)
	assert.Equal(t, errcode.ErrSessionNotFound, ec2.Code, "session must return ErrSessionNotFound")
	assert.Equal(t, 1, refreshStore.revokeUserCalls, "refresh chains must be revoked with role changes")
	assert.Equal(t, []string{"usr-2"}, refreshStore.revokeUserIDs)
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

	svc := mustNewService(t, roleRepo, failingSessionRepo{}, &trackingRefreshStore{}, slog.Default())
	err := svc.Revoke(context.Background(), "usr-1", "admin")
	require.Error(t, err, "revoke must fail-closed when session revocation fails")
	assert.Contains(t, err.Error(), "revoke sessions")
}

func TestService_Assign_SessionRevokeFail_ReturnsError(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	svc := mustNewService(t, roleRepo, failingSessionRepo{}, &trackingRefreshStore{}, slog.Default())
	err := svc.Assign(context.Background(), "usr-1", "admin")
	require.Error(t, err, "assign must fail-closed when session revocation fails")
	assert.Contains(t, err.Error(), "revoke sessions")
}

func TestService_Assign_RefreshRevokeFail_ReturnsError(t *testing.T) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	refreshErr := errors.New("refresh store unavailable")
	svc := mustNewService(t, roleRepo, testutil.RealSessionRepo(t), &trackingRefreshStore{err: refreshErr}, slog.Default())

	err := svc.Assign(context.Background(), "usr-1", "admin")
	require.Error(t, err, "assign must fail-closed when refresh revocation fails")
	assert.ErrorIs(t, err, refreshErr)
	assert.Contains(t, err.Error(), "revoke refresh chains")
}

// TestService_Assign_CallsSessionRevoke proves that sessionRepo.RevokeByUserID
// is called exactly once per Assign, invalidating the user's active sessions.
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
			sessionRepo := testutil.RealSessionRepo(t)
			// Create a session for the user so we can verify revocation.
			sess := &domain.Session{ID: "sess-" + tc.userID, UserID: tc.userID}
			require.NoError(t, sessionRepo.Create(context.Background(), sess))

			svc := mustNewService(t, roleRepo, sessionRepo, &trackingRefreshStore{}, slog.Default())
			require.NoError(t, svc.Assign(context.Background(), tc.userID, tc.roleID))

			// After P2b soft-revoke: revoked sessions are invisible via GetByID.
			_, revokeErr := sessionRepo.GetByID(context.Background(), "sess-"+tc.userID)
			require.Error(t, revokeErr, "session must be invisible after soft-revoke")
			var ec *errcode.Error
			require.ErrorAs(t, revokeErr, &ec)
			assert.Equal(t, errcode.ErrSessionNotFound, ec.Code, "demo mode: session must return ErrSessionNotFound after Assign")
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
			sessionRepo := testutil.RealSessionRepo(t)
			sess := &domain.Session{ID: "sess-" + tc.userID, UserID: tc.userID}
			require.NoError(t, sessionRepo.Create(context.Background(), sess))

			svc := mustNewService(t, roleRepo, sessionRepo, &trackingRefreshStore{}, slog.Default())
			require.NoError(t, svc.Revoke(context.Background(), tc.userID, tc.roleID))

			// After P2b soft-revoke: revoked sessions are invisible via GetByID.
			_, revokeErr := sessionRepo.GetByID(context.Background(), "sess-"+tc.userID)
			require.Error(t, revokeErr, "session must be invisible after soft-revoke")
			var ec *errcode.Error
			require.ErrorAs(t, revokeErr, &ec)
			assert.Equal(t, errcode.ErrSessionNotFound, ec.Code, "demo mode: session must return ErrSessionNotFound after Revoke")
		})
	}
}
