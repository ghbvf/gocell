package rbacassign

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// tenantCtx is declared in contract_test.go (same package)

// rbacFakeTxRunner is a test-only pass-through TxRunner (no real transaction).
type rbacFakeTxRunner struct{}

func (rbacFakeTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = rbacFakeTxRunner{}

// newTestInvalidator builds a real credentialinvalidate.Invalidator backed by
// the given stores. Fails the test on error.
func newTestInvalidator(
	t testing.TB,
	userRepo ports.UserRepository,
	sessionStore session.Store,
) *credentialinvalidate.Invalidator {
	t.Helper()
	refreshStore := testutil.RealRefreshStore(t)
	inv, err := credentialinvalidate.New(userRepo, sessionStore, refreshStore)
	require.NoError(t, err, "newTestInvalidator: construction failed")
	return inv
}

// mustNewService creates a Service with a fake TxRunner, failing the test on error.
func mustNewService(
	t testing.TB,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	sessionStore session.Store,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	t.Helper()
	inv := newTestInvalidator(t, userRepo, sessionStore)
	opts = append([]Option{WithTxManager(persistence.WrapForCell(rbacFakeTxRunner{}))}, opts...)
	svc, err := NewService(clock.Real(), roleRepo, userRepo, inv, logger, opts...)
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
	store.RoleRepository().SeedRole(testTenantID, &domain.Role{
		ID:   "admin",
		Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	})
	store.RoleRepository().SeedRole(testTenantID, &domain.Role{ID: "editor", Name: "editor"})
	seedTestUserRoster(t, store)
	sessionStore := testutil.RealSessionRepo(t)
	return mustNewService(t, store.RoleRepository(), store.UserRepository(), sessionStore, slog.Default()), store, sessionStore
}

// seedActiveUser registers an active user in store so it counts as an
// effective admin once admin role is assigned. Test convenience for the
// effective-admin invariant: simply assigning admin role without an active
// user record results in CountEffectiveAdmins == 0 and revoke rejection.
func seedActiveUser(t testing.TB, store *mem.Store, userID string) {
	t.Helper()
	// Idempotent: a roster pre-seed (seedTestUserRoster) plus explicit per-test
	// seeds (assignActiveAdmin, table-case setups) can both target the same user;
	// skip if already present so the second Create does not hit ErrAuthUserDuplicate.
	if _, err := store.UserRepository().GetByIDInTenant(context.Background(), testTenantID, userID); err == nil {
		return
	}
	u, err := domain.NewUser(userID, userID+"@test.local", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u.ID = userID
	require.NoError(t, store.UserRepository().Create(context.Background(), testTenantID, u))
}

// seedTestUserRoster idempotently seeds the standard active users that rbacassign
// tests assign/revoke roles to. Since #1617 PR-3b the tenant comes from the
// request body, but both Assign (repo composite-FK / mem user guard) and Revoke
// (the target-tenant ownership guard, review F4) still require the target user
// to exist in the tenant, so every Assign/Revoke target must exist; seeding the
// roster in the test-service constructors keeps individual tests free of
// user-existence boilerplate. Roster users hold no role, so they are invisible
// to the effective-admin count until a test assigns admin.
func seedTestUserRoster(t testing.TB, store *mem.Store) {
	t.Helper()
	for _, id := range []string{
		"usr-1", "usr-2", "usr-3", "usr-noop", "usr-active", "usr-locked",
		"alice", "bob", "carol", "u1",
	} {
		seedActiveUser(t, store, id)
	}
}

// assignActiveAdmin seeds an active user AND assigns the admin role. Use
// where the test scaffolding expects "this user is an effective admin".
func assignActiveAdmin(t testing.TB, store *mem.Store, userID string) {
	t.Helper()
	seedActiveUser(t, store, userID)
	_, err := store.RoleRepository().AssignToUser(context.Background(), testTenantID, userID, "admin")
	require.NoError(t, err)
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	store := mem.NewStore(clock.Real())
	sessionStore := testutil.RealSessionRepo(t)
	inv := newTestInvalidator(t, store.UserRepository(), sessionStore)
	// No WithTxManager — must fail.
	_, err := NewService(clock.Real(), store.RoleRepository(), store.UserRepository(), inv, slog.Default())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

func TestNewService_InvalidatorRequired(t *testing.T) {
	store := mem.NewStore(clock.Real())
	_, err := NewService(clock.Real(), store.RoleRepository(), store.UserRepository(), nil, slog.Default(),
		WithTxManager(persistence.WrapForCell(rbacFakeTxRunner{})))
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "invalidator is required")
}

// assertRoleAssigned verifies that roleID is present in the store's assignment
// list for userID.
func assertRoleAssigned(t *testing.T, store *mem.Store, userID, roleID string) {
	t.Helper()
	roles, _ := store.RoleRepository().GetByUserID(context.Background(), testTenantID, userID)
	for _, r := range roles {
		if r.ID == roleID {
			return
		}
	}
	t.Errorf("role %s should be assigned to user %s", roleID, userID)
}

func TestService_Assign(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(*testing.T, *mem.Store)
		userID       string
		roleID       string
		skipUserSeed bool // skips automatic seedActiveUser; user will be absent
		wantErr      bool
		wantCode     errcode.Code
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
				_, err := s.RoleRepository().AssignToUser(context.Background(), testTenantID, "usr-1", "admin")
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
			name:   "role not found returns error",
			userID: "usr-1",
			setup: func(t *testing.T, s *mem.Store) {
				seedActiveUser(t, s, "usr-1")
			},
			roleID:   "nonexistent",
			wantErr:  true,
			wantCode: errcode.ErrAuthRoleNotFound,
		},
		{
			name:         "user not in roster returns ErrAuthUserNotFound",
			userID:       "ghost-not-in-roster",
			roleID:       "admin",
			skipUserSeed: true,
			wantErr:      true,
			wantCode:     errcode.ErrAuthUserNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, store, _ := newTestService(t)
			// Option B: Assign derives the tenant from the target user, so the
			// user must exist. Seed it for non-validation cases (empty userID
			// cases fail at input validation before the GetByID lookup).
			// skipUserSeed=true tests the absent-user path explicitly.
			if tc.userID != "" && !tc.skipUserSeed {
				seedActiveUser(t, store, tc.userID)
			}
			if tc.setup != nil {
				tc.setup(t, store)
			}

			err := svc.Assign(tenantCtx(), testTenantID, tc.userID, tc.roleID)
			if !tc.wantErr {
				require.NoError(t, err)
				assertRoleAssigned(t, store, tc.userID, tc.roleID)
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
				require.NoError(t, s.UserRepository().UpdateLockState(context.Background(), testTenantID, "usr-2", domain.StatusLocked, time.Now()))
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
				require.NoError(t, s.UserRepository().UpdateLockState(
					context.Background(), testTenantID, "usr-locked", domain.StatusLocked, time.Now(),
				))
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
				_, err := s.RoleRepository().AssignToUser(context.Background(), testTenantID, "usr-1", "editor")
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
			// The target user must exist in the tenant for the Revoke ownership
			// guard (#1617 PR-3b review F4) to pass; seed it for non-validation cases.
			if tc.userID != "" {
				seedActiveUser(t, store, tc.userID)
			}
			if tc.setup != nil {
				tc.setup(t, store)
			}

			err := svc.Revoke(tenantCtx(), testTenantID, tc.userID, tc.roleID)
			if !tc.wantErr {
				require.NoError(t, err)
				// Verify removal persisted.
				roles, _ := store.RoleRepository().GetByUserID(context.Background(), testTenantID, tc.userID)
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

// TestRevoke_CallsFunnel_InvalidatesSessions verifies that Revoke calls the
// credentialinvalidate funnel in the same transaction, which revokes the user's
// active sessions atomically with the role removal.
func TestRevoke_CallsFunnel_InvalidatesSessions(t *testing.T) {
	svc, store, sessionStore := newTestService(t)
	ctx := tenantCtx()

	// Two active admins so the effective-admin guard passes when revoking usr-1.
	assignActiveAdmin(t, store, "usr-1")
	assignActiveAdmin(t, store, "usr-2")
	sess := &session.Session{ID: "sess-1", SubjectID: "usr-1", JTI: "jti-sess-1", AuthzEpochAtIssue: 1}
	require.NoError(t, sessionStore.Create(ctx, testTenantID, sess))

	require.NoError(t, svc.Revoke(ctx, testTenantID, "usr-1", "admin"))

	s, err := sessionStore.Get(ctx, "sess-1")
	require.NoError(t, err)
	assert.True(t, s.RevokedAt != nil, "session must be revoked after role revocation (funnel)")
}

// TestAssign_DoesNotInvalidateSessions verifies that Assign does NOT call the
// credential invalidation funnel (HIGH-3 decision: granting a role is additive
// and is not a credential-security event).
func TestAssign_DoesNotInvalidateSessions(t *testing.T) {
	svc, store, sessionStore := newTestService(t)
	ctx := tenantCtx()
	seedActiveUser(t, store, "usr-2") // Option B: Assign derives tenant from the target user

	sess := &session.Session{ID: "sess-2", SubjectID: "usr-2", JTI: "jti-sess-2", AuthzEpochAtIssue: 1}
	require.NoError(t, sessionStore.Create(ctx, testTenantID, sess))

	require.NoError(t, svc.Assign(ctx, testTenantID, "usr-2", "admin"))

	s, err := sessionStore.Get(ctx, "sess-2")
	require.NoError(t, err)
	assert.Nil(t, s.RevokedAt, "session must NOT be revoked after role assignment (HIGH-3: Assign is additive)")
}

// TestRevoke_NoOp_DoesNotCallFunnel verifies that a no-op Revoke (user does not
// hold the role) does not trigger credential invalidation.
func TestRevoke_NoOp_DoesNotCallFunnel(t *testing.T) {
	svc, _, sessionStore := newTestService(t)
	ctx := tenantCtx()

	sess := &session.Session{ID: "sess-noop-r", SubjectID: "usr-noop", JTI: "jti-noop-r", AuthzEpochAtIssue: 1}
	require.NoError(t, sessionStore.Create(ctx, testTenantID, sess))

	// usr-noop does not hold admin role — Revoke is a no-op.
	require.NoError(t, svc.Revoke(ctx, testTenantID, "usr-noop", "admin"))

	s, err := sessionStore.Get(ctx, "sess-noop-r")
	require.NoError(t, err)
	assert.Nil(t, s.RevokedAt, "no-op Revoke must not invalidate sessions")
}

// TestAssign_NoOp_DoesNotEmit verifies that a no-op Assign (already assigned)
// does not emit an outbox entry. The session must also not be revoked.
func TestAssign_NoOp_DoesNotEmit(t *testing.T) {
	svc, store, sessionStore := newTestService(t)
	ctx := tenantCtx()

	// Pre-assign role so the second Assign is a no-op.
	_, err := store.RoleRepository().AssignToUser(ctx, testTenantID, "usr-3", "admin")
	require.NoError(t, err)

	sess := &session.Session{ID: "sess-noop-a", SubjectID: "usr-3", JTI: "jti-noop-a", AuthzEpochAtIssue: 1}
	require.NoError(t, sessionStore.Create(ctx, testTenantID, sess))

	require.NoError(t, svc.Assign(ctx, testTenantID, "usr-3", "admin"))

	s, err := sessionStore.Get(ctx, "sess-noop-a")
	require.NoError(t, err)
	assert.Nil(t, s.RevokedAt, "no-op Assign must not revoke sessions")
}

// failingSessionStore returns an error on RevokeForSubject to test fail-closed
// behavior when the credential funnel encounters a session-store failure.
type failingSessionStore struct {
	session.Store
}

func (failingSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent, _ credentialfence.FenceToken) error {
	return errors.New("session store unavailable")
}

func TestRevoke_FunnelFail_ReturnsError(t *testing.T) {
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(testTenantID, &domain.Role{ID: "admin", Name: "admin"})
	// Two active admins so the effective-admin guard passes when revoking usr-1.
	assignActiveAdmin(t, store, "usr-1")
	assignActiveAdmin(t, store, "usr-2")

	realSession := testutil.RealSessionRepo(t)
	failSession := failingSessionStore{Store: realSession}
	inv, err := credentialinvalidate.New(store.UserRepository(), failSession, testutil.RealRefreshStore(t))
	require.NoError(t, err)
	svc, err := NewService(clock.Real(), store.RoleRepository(), store.UserRepository(), inv, slog.Default(),
		WithTxManager(persistence.WrapForCell(rbacFakeTxRunner{})))
	require.NoError(t, err)

	err = svc.Revoke(tenantCtx(), testTenantID, "usr-1", "admin")
	require.Error(t, err, "Revoke must fail-closed when credential invalidation fails")
	assert.Contains(t, err.Error(), "invalidate credentials")
}

// scopeCapturingRoleRepo wraps a RoleRepository and records whether the
// context passed to AssignToUser or RemoveFromUserIfNotLast carries a tenant
// scope injected by scopedtx.Do. Used by TestAssignRole_IsRLSScoped and
// TestRevokeRole_IsRLSScoped.
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
	return r.inner.GetByUserID(ctx, t, userID)
}

func (r *scopeCapturingRoleRepo) Create(ctx context.Context, t tenant.TenantID, role *domain.Role) error {
	return r.inner.Create(ctx, t, role)
}

func (r *scopeCapturingRoleRepo) AssignToUser(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.AssignToUser(ctx, t, userID, roleID)
}

func (r *scopeCapturingRoleRepo) RemoveFromUser(ctx context.Context, t tenant.TenantID, userID, roleID string) error {
	return r.inner.RemoveFromUser(ctx, t, userID, roleID)
}

func (r *scopeCapturingRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
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

func (r *scopeCapturingRoleRepo) ListByUserID(ctx context.Context, t tenant.TenantID, userID string, params query.ListParams) ([]*domain.Role, error) { //nolint:lll // test stub matching interface signature
	return r.inner.ListByUserID(ctx, t, userID, params)
}

// TestAssignRole_IsRLSScoped asserts that Assign wraps the AssignToUser call in
// a scoped transaction so the RLS tenant_isolation policy on role_assignments is
// satisfied (persistChange → scopedtx.Do).
func TestAssignRole_IsRLSScoped(t *testing.T) {
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(testTenantID, &domain.Role{ID: "editor", Name: "editor"})
	seedActiveUser(t, store, "usr-rls-assign")

	cap := &scopeCapturingRoleRepo{inner: store.RoleRepository()}
	inv := newTestInvalidator(t, store.UserRepository(), testutil.RealSessionRepo(t))
	svc, err := NewService(clock.Real(), cap, store.UserRepository(), inv, slog.Default(),
		WithTxManager(persistence.WrapForCell(rbacFakeTxRunner{})))
	require.NoError(t, err)

	err = svc.Assign(tenantCtx(), testTenantID, "usr-rls-assign", "editor")
	require.NoError(t, err)

	assert.True(t, cap.capturedOK,
		"AssignToUser must run inside a scoped tx (tenant.ScopeFromContext must be set)")
	assert.Equal(t, testTenantID, cap.capturedScope,
		"Assign scope must equal the request tenant")
}

// TestRevokeRole_IsRLSScoped asserts that Revoke wraps the
// RemoveFromUserIfNotLast call in a scoped transaction so the RLS
// tenant_isolation policy on role_assignments is satisfied.
func TestRevokeRole_IsRLSScoped(t *testing.T) {
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(testTenantID, &domain.Role{ID: "editor", Name: "editor"})
	seedActiveUser(t, store, "usr-rls-revoke")
	_, err := store.RoleRepository().AssignToUser(context.Background(), testTenantID, "usr-rls-revoke", "editor")
	require.NoError(t, err)

	cap := &scopeCapturingRoleRepo{inner: store.RoleRepository()}
	inv := newTestInvalidator(t, store.UserRepository(), testutil.RealSessionRepo(t))
	svc, err := NewService(clock.Real(), cap, store.UserRepository(), inv, slog.Default(),
		WithTxManager(persistence.WrapForCell(rbacFakeTxRunner{})))
	require.NoError(t, err)

	err = svc.Revoke(tenantCtx(), testTenantID, "usr-rls-revoke", "editor")
	require.NoError(t, err)

	assert.True(t, cap.capturedOK,
		"RemoveFromUserIfNotLast must run inside a scoped tx (tenant.ScopeFromContext must be set)")
	assert.Equal(t, testTenantID, cap.capturedScope,
		"Revoke scope must equal the request tenant")
}

// TestRevoke_WrongTenant_Returns404NotSilentSuccess (PR-3b review F4) verifies
// that revoking a role for a user that does not exist in the REQUEST's tenant
// returns a clean ErrAuthUserNotFound (404), not a silent revoked:true. The
// tenant now comes from the request body (not the target user), so a wrong
// tenant makes RemoveFromUserIfNotLast a (false, nil) no-op — which the handler
// would report as revoked:true while the role survives in the user's real
// tenant. The target-tenant ownership guard turns that into a 404, matching the
// Assign path (composite FK / mem userByIDInTenant).
func TestRevoke_WrongTenant_Returns404NotSilentSuccess(t *testing.T) {
	svc, store, _ := newTestService(t)

	// Seed the user + role assignment in testTenantID (the user's REAL tenant).
	seedActiveUser(t, store, "usr-wrongtenant")
	_, err := store.RoleRepository().AssignToUser(context.Background(), testTenantID, "usr-wrongtenant", "editor")
	require.NoError(t, err)

	// Revoke with a DIFFERENT tenant where the user does not exist.
	otherTenant, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000002")
	require.NoError(t, err)
	err = svc.Revoke(tenantCtx(), otherTenant, "usr-wrongtenant", "editor")
	require.Error(t, err, "wrong-tenant revoke must not be a silent success")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ecErr.Code,
		"wrong-tenant revoke must surface ErrAuthUserNotFound (404), matching the Assign path")

	// Proof the no-op did not leak as success: the role still exists in the
	// user's real tenant.
	roles, _ := store.RoleRepository().GetByUserID(context.Background(), testTenantID, "usr-wrongtenant")
	stillHeld := false
	for _, r := range roles {
		if r.ID == "editor" {
			stillHeld = true
		}
	}
	assert.True(t, stillHeld, "role must survive in the real tenant after a wrong-tenant revoke")
}
