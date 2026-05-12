package rbacassign

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// rbacFakeTxRunner is a test-only pass-through TxRunner (no real transaction).
type rbacFakeTxRunner struct{}

func (rbacFakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = rbacFakeTxRunner{}

// mustNewService creates a Service with a fake TxRunner, failing the test on error.
func mustNewService(
	t testing.TB,
	roleRepo ports.RoleRepository,
	sessionStore session.Store,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	t.Helper()
	opts = append([]Option{WithTxManager(persistence.WrapForCell(rbacFakeTxRunner{}))}, opts...)
	svc, err := NewService(roleRepo, sessionStore, logger, opts...)
	require.NoError(t, err)
	return svc
}

// newTestService constructs a Service backed by a shared mem.Store so the
// rbacassign RemoveFromUserIfNotLast admin path can observe user.Status
// (effective-admin invariant, S4.0). Returns the store so callers can seed
// active user records when staging admin role assignments.
func newTestService(t testing.TB) (*Service, *mem.Store, *session.MemStore) {
	t.Helper()
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	store.RoleRepository().SeedRole(&domain.Role{ID: "editor", Name: "editor"})
	sessionStore := testutil.RealSessionRepo(t)
	return mustNewService(t, store.RoleRepository(), sessionStore, slog.Default()), store, sessionStore
}

// seedActiveUser registers an active user in store so it counts as an
// effective admin once admin role is assigned. Test convenience for the
// effective-admin invariant: simply assigning admin role without an active
// user record results in CountEffectiveAdmins == 0 and revoke rejection.
func seedActiveUser(t testing.TB, store *mem.Store, userID string) {
	t.Helper()
	require.NoError(t, store.UserRepository().Create(context.Background(), &domain.User{
		ID:       userID,
		Username: userID,
		Email:    userID + "@test.local",
		Status:   domain.StatusActive,
	}))
}

// assignActiveAdmin seeds an active user AND assigns the admin role. Use
// where the test scaffolding expects "this user is an effective admin".
func assignActiveAdmin(t testing.TB, store *mem.Store, userID string) {
	t.Helper()
	seedActiveUser(t, store, userID)
	_, err := store.RoleRepository().AssignToUser(context.Background(), userID, "admin")
	require.NoError(t, err)
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	sessionStore := testutil.RealSessionRepo(t)
	_, err := NewService(roleRepo, sessionStore, slog.Default() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

func TestService_Assign(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*testing.T, *mem.Store)
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
			setup: func(t *testing.T, s *mem.Store) {
				_, err := s.RoleRepository().AssignToUser(context.Background(), "usr-1", "admin")
				require.NoError(t, err)
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
			svc, store, _ := newTestService(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			err := svc.Assign(context.Background(), tc.userID, tc.roleID)
			if !tc.wantErr {
				require.NoError(t, err)
				// Verify assignment persisted.
				roles, _ := store.RoleRepository().GetByUserID(context.Background(), tc.userID)
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
		setup    func(*testing.T, *mem.Store)
		userID   string
		roleID   string
		wantErr  bool
		wantCode errcode.Code
	}{
		{
			name:   "revoke assigned role with multiple active admin holders",
			userID: "usr-1",
			roleID: "admin",
			setup: func(t *testing.T, s *mem.Store) {
				assignActiveAdmin(t, s, "usr-1")
				assignActiveAdmin(t, s, "usr-2")
			},
			wantErr: false,
		},
		{
			name:   "revoke sole effective admin returns ErrAuthLastAdminProtected",
			userID: "usr-1",
			roleID: "admin",
			setup: func(t *testing.T, s *mem.Store) {
				assignActiveAdmin(t, s, "usr-1")
			},
			wantErr:  true,
			wantCode: errcode.ErrAuthLastAdminProtected,
		},
		{
			// S4.0 effective-admin upgrade: a locked admin peer is NOT a usable
			// fallback, so revoking the *active* admin must be refused.
			name:   "revoke active admin when only peer is locked is refused",
			userID: "usr-1",
			roleID: "admin",
			setup: func(t *testing.T, s *mem.Store) {
				assignActiveAdmin(t, s, "usr-1")
				assignActiveAdmin(t, s, "usr-2")
				// Lock usr-2 — now usr-1 is the sole effective admin.
				u, err := s.UserRepository().GetByID(context.Background(), "usr-2")
				require.NoError(t, err)
				u.Status = domain.StatusLocked
				require.NoError(t, s.UserRepository().Update(context.Background(), u))
			},
			wantErr:  true,
			wantCode: errcode.ErrAuthLastAdminProtected,
		},
		{
			// Inverse: revoking a *locked* admin does not reduce the effective
			// admin count, so it must succeed even when the active peer is the
			// only other holder.
			name:   "revoke locked admin while active admin remains is allowed",
			userID: "usr-locked",
			roleID: "admin",
			setup: func(t *testing.T, s *mem.Store) {
				assignActiveAdmin(t, s, "usr-active")
				assignActiveAdmin(t, s, "usr-locked")
				u, err := s.UserRepository().GetByID(context.Background(), "usr-locked")
				require.NoError(t, err)
				u.Status = domain.StatusLocked
				require.NoError(t, s.UserRepository().Update(context.Background(), u))
			},
			wantErr: false,
		},
		{
			// ADR-admin-invariant §3.2: last-holder guard is admin-scoped.
			// Non-admin roles must be revocable to zero holders.
			name:   "revoke last non-admin holder is allowed (admin-scoped guard)",
			userID: "usr-1",
			roleID: "editor",
			setup: func(t *testing.T, s *mem.Store) {
				seedActiveUser(t, s, "usr-1")
				_, err := s.RoleRepository().AssignToUser(context.Background(), "usr-1", "editor")
				require.NoError(t, err)
			},
			wantErr: false,
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
			svc, store, _ := newTestService(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}

			err := svc.Revoke(context.Background(), tc.userID, tc.roleID)
			if !tc.wantErr {
				require.NoError(t, err)
				// Verify removal persisted.
				roles, _ := store.RoleRepository().GetByUserID(context.Background(), tc.userID)
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
	svc, store, sessionStore := newTestService(t)
	ctx := context.Background()

	// Two active admins so the effective-admin guard passes when revoking usr-1.
	assignActiveAdmin(t, store, "usr-1")
	assignActiveAdmin(t, store, "usr-2")
	sess := &session.Session{ID: "sess-1", SubjectID: "usr-1", JTI: "jti-sess-1"}
	require.NoError(t, sessionStore.Create(ctx, sess))

	require.NoError(t, svc.Revoke(ctx, "usr-1", "admin"))

	s, err := sessionStore.Get(ctx, "sess-1")
	require.NoError(t, err)
	assert.True(t, s.RevokedAt != nil, "session must be revoked after role change")
}

func TestService_Assign_InvalidatesSessions(t *testing.T) {
	svc, _, sessionStore := newTestService(t)
	ctx := context.Background()

	sess := &session.Session{ID: "sess-2", SubjectID: "usr-2", JTI: "jti-sess-2"}
	require.NoError(t, sessionStore.Create(ctx, sess))

	require.NoError(t, svc.Assign(ctx, "usr-2", "admin"))

	s, err := sessionStore.Get(ctx, "sess-2")
	require.NoError(t, err)
	assert.True(t, s.RevokedAt != nil, "session must be revoked after role assignment")
}

// failingSessionStore returns an error on RevokeForSubject to test fail-closed behavior.
type failingSessionStore struct{}

func (failingSessionStore) Create(_ context.Context, _ *session.Session) error {
	return nil
}

func (failingSessionStore) Get(_ context.Context, _ string) (*session.Session, error) {
	return nil, fmt.Errorf("session store unavailable")
}

func (failingSessionStore) Revoke(_ context.Context, _ string) error {
	return nil
}

func (failingSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	return fmt.Errorf("session store unavailable")
}

var _ session.Store = failingSessionStore{}

func TestService_Revoke_SessionRevokeFail_ReturnsError(t *testing.T) {
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{ID: "admin", Name: "admin"})
	// Two active admins so the effective-admin guard passes when revoking usr-1.
	assignActiveAdmin(t, store, "usr-1")
	assignActiveAdmin(t, store, "usr-2")

	svc := mustNewService(t, store.RoleRepository(), failingSessionStore{}, slog.Default())
	err := svc.Revoke(context.Background(), "usr-1", "admin")
	require.Error(t, err, "revoke must fail-closed when session revocation fails")
	assert.Contains(t, err.Error(), "session revoke failed")
}

func TestService_Assign_SessionRevokeFail_ReturnsError(t *testing.T) {
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	svc := mustNewService(t, roleRepo, failingSessionStore{}, slog.Default())
	err := svc.Assign(context.Background(), "usr-1", "admin")
	require.Error(t, err, "assign must fail-closed when session revocation fails")
	assert.Contains(t, err.Error(), "session revoke failed")
}

// TestService_DemoMode_Assign_CallsSessionRevoke proves that sessionStore.RevokeForSubject
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
			roleRepo := mem.NewStore(clock.Real()).RoleRepository()
			roleRepo.SeedRole(&domain.Role{
				ID:          "admin",
				Name:        "admin",
				Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
			})
			sessionStore := testutil.RealSessionRepo(t)
			// Create a session for the user so we can verify revocation.
			sess := &session.Session{ID: "sess-" + tc.userID, SubjectID: tc.userID, JTI: "jti-" + tc.userID}
			require.NoError(t, sessionStore.Create(context.Background(), sess))

			svc := mustNewService(t, roleRepo, sessionStore, slog.Default())
			require.NoError(t, svc.Assign(context.Background(), tc.userID, tc.roleID))

			s, err := sessionStore.Get(context.Background(), "sess-"+tc.userID)
			require.NoError(t, err)
			assert.True(t, s.RevokedAt != nil, "demo mode: session must be revoked after Assign")
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
			store := mem.NewStore(clock.Real())
			store.RoleRepository().SeedRole(&domain.Role{
				ID:          "admin",
				Name:        "admin",
				Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
			})
			// Two active admins so the effective-admin guard passes when
			// revoking tc.userID.
			assignActiveAdmin(t, store, tc.userID)
			assignActiveAdmin(t, store, "usr-other")
			sessionStore := testutil.RealSessionRepo(t)
			sess := &session.Session{ID: "sess-" + tc.userID, SubjectID: tc.userID, JTI: "jti-" + tc.userID}
			require.NoError(t, sessionStore.Create(context.Background(), sess))

			svc := mustNewService(t, store.RoleRepository(), sessionStore, slog.Default())
			require.NoError(t, svc.Revoke(context.Background(), tc.userID, tc.roleID))

			s, err := sessionStore.Get(context.Background(), "sess-"+tc.userID)
			require.NoError(t, err)
			assert.True(t, s.RevokedAt != nil, "demo mode: session must be revoked after Revoke")
		})
	}
}
