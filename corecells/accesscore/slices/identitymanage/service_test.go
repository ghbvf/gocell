package identitymanage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// adminCtxForService returns a context with an admin principal and canonical test
// tenant for service-layer tests. All write paths (Lock, Unlock, Update, Delete)
// require a non-empty subject and a valid tenant (#1337 PR-2).
func adminCtxForService() context.Context {
	return withTenant(auth.TestContext("test-admin", []string{"admin"}))
}

// inertRoleRepo returns a fresh, empty RoleRepository from a SEPARATE mem.Store
// (deliberately not the test's user store). Because checkLastAdminRemoval reads
// admin-role membership from this repo via GetByUserID, an empty+isolated repo
// reports no admins for any user → the mandatory last-admin guard is constructed
// but never trips, regardless of what the test's own UserRepository contains.
// This makes it behavior-preserving for tests that do not exercise last-admin
// protection (the old code had no guard at all on these paths).
//
// Tests that DO exercise last-admin protection must instead use
// newLastAdminProtectedService, which wires a real, admin-seeded roleRepo from
// the SAME store as the user repo so CountEffectiveAdmins sees both.
func inertRoleRepo() ports.RoleRepository {
	return mem.NewStore(clock.Real()).RoleRepository()
}

// minimalStubIssuer is a zero-config TokenIssuer stub used by tests that only
// exercise non-ChangePassword paths (Create, Update, Lock, etc.) and do not
// care about the token pair content.
var minimalStubIssuer TokenIssuer = &stubTokenIssuer{}

// simpleTxRunner is a test-only pass-through TxRunner. It holds no lock and
// passes ctx through unchanged, so each repo method takes its own per-call
// lock (race-safe under concurrent goroutines). It does NOT provide
// cross-method atomicity (GetByID→…→UpdatePassword are independently locked);
// tests needing a whole-closure atomic tx must wire mem.Store.TxRunner()
// instead. See ADR docs/architecture/202605171846-adr-mem-tx-lock-ownership.md.
type simpleTxRunner struct{}

func (simpleTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = simpleTxRunner{}

func newTestService(t testing.TB) *Service {
	t.Helper()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	if err != nil {
		panic("newTestService: " + err.Error())
	}
	return svc
}

// TestNewService_TxRunnerRequired asserts that NewService fails fast with
// errcode.ErrCellInvalidConfig when WithTxManager is omitted (nil TxRunner).
// Wiring failure is operator error → 5xx, not client 4xx.
// Symmetric with the other outbox-bound services per REQUIRED-DEP-NIL-GUARD-01.
func TestNewService_TxRunnerRequired(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer) /* no WithTxManager */)
	require.Error(t, err)
	assert.Nil(t, svc)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

// TestNewService_RoleRepoRequired asserts that the required positional roleRepo
// param fails fast when nil — completing the required-dep test symmetry with
// TxRunner / tokenIssuer. roleRepo is positional (not a gocell:"required" tag),
// so its guard lives in buildLastAdminGuard rather than validateRequired.
func TestNewService_RoleRepoRequired(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
		nil, // nil roleRepo — must be rejected by buildLastAdminGuard
		WithTokenIssuer(minimalStubIssuer), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.Error(t, err, "NewService with nil roleRepo must fail")
	assert.Nil(t, svc)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "role repository")
}

// TestNewService_RequiresTokenIssuer asserts that NewService returns a non-nil
// error when WithTokenIssuer is omitted or nil, enforcing fail-fast wiring.
func TestNewService_RequiresTokenIssuer(t *testing.T) {
	t.Run("no WithTokenIssuer option", func(t *testing.T) {
		userRepo := mem.NewStore(clock.Real()).UserRepository()
		sessionStore := testutil.RealSessionRepo(t)
		refreshStore := newIdentityRefreshStore()
		svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
			inertRoleRepo(),
			WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
		require.Error(t, err, "NewService without WithTokenIssuer must fail")
		assert.Nil(t, svc)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrCellMissingTokenIssuer, ec.Code)
	})

	t.Run("WithTokenIssuer(nil)", func(t *testing.T) {
		userRepo := mem.NewStore(clock.Real()).UserRepository()
		sessionStore := testutil.RealSessionRepo(t)
		refreshStore := newIdentityRefreshStore()
		svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
			inertRoleRepo(),
			WithTokenIssuer(nil), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
		require.Error(t, err, "NewService with nil tokenIssuer must fail")
		assert.Nil(t, svc)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrCellMissingTokenIssuer, ec.Code)
	})
}

func TestService_Create(t *testing.T) {
	tests := []struct {
		name    string
		input   CreateInput
		wantErr bool
	}{
		{name: "valid", input: CreateInput{Username: "alice", Email: "a@b.c", Password: "hash"}, wantErr: false},
		{name: "empty username", input: CreateInput{Username: "", Email: "a@b.c", Password: "hash"}, wantErr: true},
		{name: "empty email", input: CreateInput{Username: "alice", Email: "", Password: "hash"}, wantErr: true},
		{name: "empty password", input: CreateInput{Username: "alice", Email: "a@b.c", Password: ""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newTestService(t)
			user, err := svc.Create(adminCtxForService(), tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, user.ID)
				assert.Equal(t, tt.input.Username, user.Username)
			}
		})
	}
}

func TestService_LockUnlock(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "bob", Email: "b@c.d", Password: "hash",
	})
	require.NoError(t, err)

	// Lock
	require.NoError(t, svc.Lock(adminCtxForService(), user.ID))
	locked, _ := svc.GetByID(adminCtxForService(), user.ID)
	assert.True(t, locked.IsLocked())

	// Unlock
	require.NoError(t, svc.Unlock(adminCtxForService(), user.ID))
	unlocked, _ := svc.GetByID(adminCtxForService(), user.ID)
	assert.False(t, unlocked.IsLocked())
}

func TestService_Lock_RevokesSession(t *testing.T) {
	sessionRepo := testutil.RealSessionRepo(t)
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "carol", Email: "c@d.e", Password: "hash",
	})
	require.NoError(t, err)

	// Seed a session for this user.
	sess := &session.Session{
		ID:                "sess-carol",
		SubjectID:         user.ID,
		JTI:               "jti-carol",
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}
	require.NoError(t, sessionRepo.Create(context.Background(), testTenantID, sess))

	// Lock the user — sessions should be revoked.
	require.NoError(t, svc.Lock(adminCtxForService(), user.ID))

	// Verify session was revoked.
	got, err := sessionRepo.Get(context.Background(), "sess-carol")
	require.NoError(t, err)
	assert.True(t, got.RevokedAt != nil, "session should be revoked after user lock")
}

func TestService_Delete(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.Create(adminCtxForService(), CreateInput{
		Username: "del", Email: "d@e.f", Password: "hash",
	})

	require.NoError(t, svc.Delete(adminCtxForService(), user.ID))
	_, err := svc.GetByID(adminCtxForService(), user.ID)
	assert.Error(t, err)
}

func newLastAdminProtectedService(t testing.TB) (*Service, *mem.UserRepository, *mem.RoleRepository) {
	t.Helper()
	// Shared store so user.Status and role assignments are atomically visible
	// to the effective-admin counter (S4.0). Two separate Stores would silently
	// break the invariant: CountEffectiveAdmins would see role rows but no
	// user records and always return 0.
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	require.NoError(t, roleRepo.Create(context.Background(), testTenantID, &domain.Role{
		ID:   auth.RoleAdmin,
		Name: auth.RoleAdmin,
	}))
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(
		clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, refreshStore), slog.Default(),
		roleRepo,
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})),
	)
	require.NoError(t, err)
	return svc, userRepo, roleRepo
}

func assignAdminForIdentityManageTest(t testing.TB, roleRepo *mem.RoleRepository, userID string) {
	t.Helper()
	_, err := roleRepo.AssignToUser(context.Background(), testTenantID, userID, auth.RoleAdmin)
	require.NoError(t, err)
}

func assertLastAdminProtected(t testing.TB, err error) {
	t.Helper()
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthLastAdminProtected, ec.Code)
}

func TestService_Delete_LastAdminProtected(t *testing.T) {
	svc, userRepo, roleRepo := newLastAdminProtectedService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "last-admin-delete", Email: "last-admin-delete@example.com", Password: "hash",
	})
	require.NoError(t, err)
	assignAdminForIdentityManageTest(t, roleRepo, user.ID)

	err = svc.Delete(adminCtxForService(), user.ID)

	assertLastAdminProtected(t, err)
	_, getErr := userRepo.GetByIDInTenant(context.Background(), testTenantID, user.ID)
	require.NoError(t, getErr, "last-admin-protected delete must leave the user row intact")
}

func TestService_Lock_LastAdminProtected(t *testing.T) {
	svc, userRepo, roleRepo := newLastAdminProtectedService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "last-admin-lock", Email: "last-admin-lock@example.com", Password: "hash",
	})
	require.NoError(t, err)
	assignAdminForIdentityManageTest(t, roleRepo, user.ID)

	err = svc.Lock(adminCtxForService(), user.ID)

	assertLastAdminProtected(t, err)
	persisted, getErr := userRepo.GetByIDInTenant(context.Background(), testTenantID, user.ID)
	require.NoError(t, getErr)
	assert.False(t, persisted.IsLocked(), "last-admin-protected lock must not update the user")
}

func TestService_Update_LastAdminProtected_StatusDemotion(t *testing.T) {
	svc, userRepo, roleRepo := newLastAdminProtectedService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "last-admin-update", Email: "last-admin-update@example.com", Password: "hash",
	})
	require.NoError(t, err)
	assignAdminForIdentityManageTest(t, roleRepo, user.ID)

	suspended := string(domain.StatusSuspended)
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Status: &suspended})

	assertLastAdminProtected(t, err)
	persisted, getErr := userRepo.GetByIDInTenant(context.Background(), testTenantID, user.ID)
	require.NoError(t, getErr)
	assert.Equal(t, domain.StatusActive, persisted.Status(),
		"last-admin-protected update must not change the user's status")
}

func TestService_Update_LastAdminAllowedWhenAnotherActiveAdminRemains(t *testing.T) {
	svc, _, roleRepo := newLastAdminProtectedService(t)
	first, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "admin-one-upd", Email: "admin-one-upd@example.com", Password: "hash",
	})
	require.NoError(t, err)
	second, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "admin-two-upd", Email: "admin-two-upd@example.com", Password: "hash",
	})
	require.NoError(t, err)
	assignAdminForIdentityManageTest(t, roleRepo, first.ID)
	assignAdminForIdentityManageTest(t, roleRepo, second.ID)

	suspended := string(domain.StatusSuspended)
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: first.ID, Status: &suspended})
	require.NoError(t, err)
}

func TestService_Delete_LastAdminAllowedWhenAnotherAdminRemains(t *testing.T) {
	svc, userRepo, roleRepo := newLastAdminProtectedService(t)
	first, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "admin-one", Email: "admin-one@example.com", Password: "hash",
	})
	require.NoError(t, err)
	second, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "admin-two", Email: "admin-two@example.com", Password: "hash",
	})
	require.NoError(t, err)
	assignAdminForIdentityManageTest(t, roleRepo, first.ID)
	assignAdminForIdentityManageTest(t, roleRepo, second.ID)

	require.NoError(t, svc.Delete(adminCtxForService(), first.ID))

	_, getErr := userRepo.GetByIDInTenant(context.Background(), testTenantID, first.ID)
	require.Error(t, getErr)
}

func TestService_Update(t *testing.T) {
	svc := newTestService(t)
	user, _ := svc.Create(adminCtxForService(), CreateInput{
		Username: "upd", Email: "old@e.f", Password: "hash",
	})

	newEmail := domain.NonEmpty("new@e.f")
	updated, err := svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err)
	assert.Equal(t, "new@e.f", updated.Email)
}

// (TestService_Update_RejectsEmptyStringPATCH removed: empty-string PATCH is
// now type-system unrepresentable. *domain.NonEmpty constructors (NewNonEmpty
// / UnmarshalJSON) reject ""; coverage moved to
// corecells/accesscore/internal/domain/nonempty_test.go.)

// TestService_Update_StatusRequiresAdminRole covers the S4.0 P1-A
// field-level guard: a non-admin caller cannot mutate user.Status even
// though the PATCH route policy is selfOrAdminPolicy. Pre-S4.0 a
// suspended user could self-PATCH back to active and defeat suspend.
func TestService_Update_StatusRequiresAdminRole(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "self-patch", Email: "self@e.f", Password: "hash",
	})
	require.NoError(t, err)

	// Self-PATCH with non-admin roles: changing status must fail with 403.
	nonAdminCtx := withTenant(auth.TestContext(user.ID, []string{"user"}))
	active := "active"
	_, err = svc.Update(nonAdminCtx, UpdateInput{ID: user.ID, Status: &active})
	require.Error(t, err, "non-admin must not be able to mutate status")
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
	assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)

	// Same caller updating a non-status field MUST succeed (field-level guard
	// applies only to status).
	newEmail := domain.NonEmpty("user-self@e.f")
	updated, err := svc.Update(nonAdminCtx, UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err, "non-admin self-PATCH of non-status fields must succeed")
	assert.Equal(t, "user-self@e.f", updated.Email)
}

// TestService_Update_StatusAllowsSuperAdminAuthority covers PR #1974 review F1:
// PR-10c widened the user:write baseline to admit super-admin (adminOrSuperAdmin),
// so the S4.0 P1-A field-level status guard must accept admin authority — admin
// OR super-admin — not admin alone. A pure super-admin caller (not the admin role,
// not the target subject) mutating status must succeed; before the fix it cleared
// the route gate but was then wrongly 403'd by the admin-only field check, leaving
// the route PDP and the service-layer guard semantically split.
func TestService_Update_StatusAllowsSuperAdminAuthority(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "target-user", Email: "target@e.f", Password: "hash",
	})
	require.NoError(t, err)

	// Pure super-admin caller: holds RoleSuperAdmin only (no admin role) and is a
	// DIFFERENT subject than the target, so neither the admin literal nor the
	// self-exemption applies — only admin AUTHORITY (admin ∪ super-admin) lets it
	// through the field guard.
	superAdminCtx := withTenant(auth.TestContext("test-superadmin", []string{auth.RoleSuperAdmin}))
	suspended := string(domain.StatusSuspended)
	updated, err := svc.Update(superAdminCtx, UpdateInput{ID: user.ID, Status: &suspended})
	require.NoError(t, err, "super-admin authority must satisfy the status field guard (PR #1974 F1)")
	assert.Equal(t, domain.StatusSuspended, updated.Status())
}

// TestService_Update_SuspendCascadeRevokesSessionsAndRefresh covers the
// S4.0 P1-A cascade-revoke path: when admin demotes an active user to
// suspended via Update, the user's live sessions + refresh chains are
// revoked atomically with the row update (mirrors Lock's revoke cascade).
func TestService_Update_SuspendCascadeRevokesSessionsAndRefresh(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "to-suspend", Email: "ts@e.f", Password: "hash",
	})
	require.NoError(t, err)

	// Seed an active session for the user; if cascade-revoke fires it will be
	// marked revoked by sessionStore.RevokeForSubject.
	sessID := "sess-suspend-" + user.ID
	seedSess := &session.Session{
		ID:                sessID,
		SubjectID:         user.ID,
		JTI:               "jti-suspend-" + user.ID,
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}
	require.NoError(t, sessionRepo.Create(context.Background(), testTenantID, seedSess))

	suspended := "suspended"
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Status: &suspended})
	require.NoError(t, err, "admin status demotion to suspended must succeed")

	// Cascade side effect: session must be revoked.
	postSess, err := sessionRepo.Get(context.Background(), sessID)
	require.NoError(t, err)
	assert.True(t, postSess.RevokedAt != nil,
		"Update demotion from active must cascade-revoke the user's sessions (S4.0 P1-A)")
}

// TestService_Update_StatusUnchanged_NoCascadeRevoke covers the
// isStatusDemotion=false branch: when the Update does NOT demote status
// (email-only patch, or active→active), the cascade-revoke MUST NOT fire
// (otherwise every Update would log users out).
func TestService_Update_StatusUnchanged_NoCascadeRevoke(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "no-revoke", Email: "nr@e.f", Password: "hash",
	})
	require.NoError(t, err)

	sessID := "sess-norevoke-" + user.ID
	seedSess := &session.Session{
		ID:                sessID,
		SubjectID:         user.ID,
		JTI:               "jti-norevoke-" + user.ID,
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}
	require.NoError(t, sessionRepo.Create(context.Background(), testTenantID, seedSess))

	newEmail := domain.NonEmpty("nr2@e.f")
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err)

	postSess, err := sessionRepo.Get(context.Background(), sessID)
	require.NoError(t, err)
	assert.False(t, postSess.RevokedAt != nil,
		"email-only Update must not cascade-revoke (only status demotion does)")
}

// stubTokenIssuer is a test double for TokenIssuer. calls records how many
// times IssueForUser was invoked so tests can assert the post-commit token
// issue does NOT run when an upstream gate rejects (see #1017). calls is
// atomic because the package-level minimalStubIssuer is shared across tests,
// including the concurrent goroutines in identitymanage_credential_race_test.go.
type stubTokenIssuer struct {
	pair  dto.TokenPair
	err   error
	calls atomic.Int64
}

func (s *stubTokenIssuer) IssueForUser(_ context.Context, _ string) (dto.TokenPair, error) {
	s.calls.Add(1)
	return s.pair, s.err
}

// seedUserWithHash creates a user in the repo with a known bcrypt hash.
func seedUserWithHash(t *testing.T, repo *mem.UserRepository, username, password string, markReset bool) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := domain.NewUser(username, username+"@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-" + username
	if markReset {
		user.SetPasswordResetRequired(true, time.Now())
	}
	require.NoError(t, repo.Create(context.Background(), testTenantID, user))
	return user
}

// seedInactiveUserWithHash creates a user with a known bcrypt hash and a
// non-active account status (suspended/locked) persisted before Create, so the
// ChangePassword inactive-gate regression (#1017) sees a real inactive row.
func seedInactiveUserWithHash(t *testing.T, repo *mem.UserRepository, username, password string, status domain.UserStatus) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := domain.NewUser(username, username+"@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-" + username
	user.SetStatus(status, time.Now())
	require.NoError(t, repo.Create(context.Background(), testTenantID, user))
	return user
}

func TestService_Update_PatchSemantics(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "patch", Email: "p@e.f", Password: "hash",
	})
	require.NoError(t, err)

	// Update only name, email should stay unchanged.
	newName := domain.NonEmpty("patchedName")
	updated, err := svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, "patchedName", updated.Username)
	assert.Equal(t, "p@e.f", updated.Email)

	// Update status to suspended.
	suspended := "suspended"
	updated, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Status: &suspended})
	require.NoError(t, err)
	assert.Equal(t, "suspended", string(updated.Status()))

	// Invalid status should fail.
	badStatus := "deleted"
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Status: &badStatus})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// Narrow write-method field-isolation tests (issue #828)
//
// These pin the use-case-specific column sets at the service layer; the
// conformance suite covers the same invariants at the repo layer. Together
// they double-lock that splitting Update(*User) into narrow methods kept the
// per-call column boundary intact.
// ---------------------------------------------------------------------------

// TestService_UpdateProfile_DoesNotTouchAuthzFields verifies that an
// email-only PATCH through Update routes to UpdateProfile and does NOT alter
// status / passwordResetRequired / passwordHash.
func TestService_UpdateProfile_DoesNotTouchAuthzFields(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "profiso", Email: "before@e.f", Password: "hash",
	})
	require.NoError(t, err)

	// Move user to Suspended via admin PATCH so we have a non-default
	// status to assert is preserved by a subsequent profile-only PATCH.
	suspended := "suspended"
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Status: &suspended})
	require.NoError(t, err)

	// Snapshot password-derived fields after a stable read.
	pre, err := svc.GetByID(adminCtxForService(), user.ID)
	require.NoError(t, err)
	preHash := pre.PasswordHash
	prePV := pre.PasswordVersion
	preEpoch := pre.AuthzEpoch()
	preReset := pre.PasswordResetRequired()
	preFailedCount := pre.FailedLoginCount()
	preLastFailedAt := pre.LastFailedAt()
	preLockedUntil := pre.AutoLockoutDeadline()

	newEmail := domain.NonEmpty("after@e.f")
	updated, err := svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err)

	assert.Equal(t, "after@e.f", updated.Email)
	assert.Equal(t, domain.StatusSuspended, updated.Status(),
		"profile-only PATCH must not change status")
	assert.Equal(t, preReset, updated.PasswordResetRequired(),
		"profile-only PATCH must not change passwordResetRequired")
	assert.Equal(t, preHash, updated.PasswordHash,
		"profile-only PATCH must not change passwordHash")
	assert.Equal(t, prePV, updated.PasswordVersion,
		"profile-only PATCH must not change passwordVersion")
	assert.Equal(t, preEpoch, updated.AuthzEpoch(),
		"profile-only PATCH must not change authzEpoch")
	assert.Equal(t, preFailedCount, updated.FailedLoginCount(), "profile-only PATCH must not change failed_login_count")
	assert.Equal(t, preLastFailedAt, updated.LastFailedAt(), "profile-only PATCH must not change last_failed_at")
	assert.Equal(t, preLockedUntil, updated.AutoLockoutDeadline(), "profile-only PATCH must not change locked_until")
}

// TestService_Update_ProfileAndStatusCombined pins the user-visible behavior of
// a combined PATCH: when both Name and Status are set in the same request the
// returned aggregate must carry both the new name AND the new status.
//
// Note: combined status+requirePasswordReset is rejected (see
// TestService_Update_CombinedAuthzFields_Rejected), but status+profile fields
// are valid together.
func TestService_Update_ProfileAndStatusCombined(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "combined", Email: "combined@e.f", Password: "hash",
	})
	require.NoError(t, err)

	newName := domain.NonEmpty("combined-new")
	suspended := "suspended"
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:     user.ID,
		Name:   &newName,
		Status: &suspended,
	})
	require.NoError(t, err)
	assert.Equal(t, "combined-new", updated.Username, "combined PATCH must apply new name")
	assert.Equal(t, domain.StatusSuspended, updated.Status(), "combined PATCH must apply new status")
}

// TestService_Lock_DoesNotTouchProfile verifies Lock routes to UpdateLockState
// without disturbing username / email / password fields.
func TestService_Lock_DoesNotTouchProfile(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "lockiso", Email: "lockiso@e.f", Password: "hash",
	})
	require.NoError(t, err)

	pre, err := svc.GetByID(adminCtxForService(), user.ID)
	require.NoError(t, err)
	preName := pre.Username
	preEmail := pre.Email
	preHash := pre.PasswordHash
	prePV := pre.PasswordVersion

	require.NoError(t, svc.Lock(adminCtxForService(), user.ID))

	post, err := svc.GetByID(adminCtxForService(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusLocked, post.Status(), "Lock must persist status=Locked")
	assert.Equal(t, preName, post.Username, "Lock must not change username")
	assert.Equal(t, preEmail, post.Email, "Lock must not change email")
	assert.Equal(t, preHash, post.PasswordHash, "Lock must not change passwordHash")
	assert.Equal(t, prePV, post.PasswordVersion, "Lock must not change passwordVersion")
}

// TestService_Update_RequirePasswordReset_DoesNotTouchProfile verifies that
// PATCH with requirePasswordReset=true routes to UpdatePasswordResetFlag (via
// authzmutate) and does NOT alter username / email / passwordHash.
func TestService_Update_RequirePasswordReset_DoesNotTouchProfile(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "resetiso", Email: "resetiso@e.f", Password: "hash",
	})
	require.NoError(t, err)

	pre, err := svc.GetByID(adminCtxForService(), user.ID)
	require.NoError(t, err)
	preName := pre.Username
	preEmail := pre.Email
	preHash := pre.PasswordHash
	prePV := pre.PasswordVersion
	require.False(t, pre.PasswordResetRequired())

	requireReset := true
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID: user.ID, RequirePasswordReset: &requireReset,
	})
	require.NoError(t, err)

	assert.True(t, updated.PasswordResetRequired(),
		"PATCH requirePasswordReset=true must set the flag")
	assert.Equal(t, preName, updated.Username, "requirePasswordReset PATCH must not change username")
	assert.Equal(t, preEmail, updated.Email, "requirePasswordReset PATCH must not change email")
	assert.Equal(t, preHash, updated.PasswordHash, "requirePasswordReset PATCH must not change passwordHash")
	assert.Equal(t, prePV, updated.PasswordVersion, "requirePasswordReset PATCH must not change passwordVersion")
}

// ---------------------------------------------------------------------------
// ChangePassword tests
// ---------------------------------------------------------------------------

func newServiceWithIssuer(t testing.TB, issuer TokenIssuer) (*Service, *mem.UserRepository) {
	t.Helper()
	repo := mem.NewStore(clock.Real()).UserRepository()
	effectiveIssuer := issuer
	if effectiveIssuer == nil {
		effectiveIssuer = minimalStubIssuer
	}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(effectiveIssuer), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	if err != nil {
		panic("newServiceWithIssuer: " + err.Error())
	}
	return svc, repo
}

func TestService_ChangePassword_VerifyOldPasswordOk(t *testing.T) {
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "new-at", RefreshToken: "new-rt"}}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cp-ok", "oldpass", false)

	pair, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-ok",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)
	assert.Equal(t, "new-at", pair.AccessToken)

	// Verify stored hash changed.
	updated, _ := repo.GetByIDInTenant(context.Background(), testTenantID, "usr-cp-ok")
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("newpass")))
	assert.False(t, updated.PasswordResetRequired(), "flag must be cleared after password change")
}

func TestService_ChangePassword_VerifyOldPasswordFail(t *testing.T) {
	stub := &stubTokenIssuer{}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cp-bad", "correctpass", false)

	_, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-bad",
		OldPassword: "wrongpass",
		NewPassword: "newpass",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old password incorrect")

	// No side effects: hash unchanged.
	orig, _ := repo.GetByIDInTenant(context.Background(), testTenantID, "usr-cp-bad")
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(orig.PasswordHash), []byte("correctpass")))
}

// TestService_ChangePassword_InactiveUser_RejectsPreMutation pins the #1017
// fix: a suspended/locked account's ChangePassword must be rejected by the
// credentialauthority.Assert gate BEFORE any credential mutation — the old hash
// must not be rewritten, PasswordVersion must not advance, and the post-commit
// token issue (IssueForUser) must not run.
func TestService_ChangePassword_InactiveUser_RejectsPreMutation(t *testing.T) {
	cases := []struct {
		name   string
		status domain.UserStatus
	}{
		{name: "suspended", status: domain.StatusSuspended},
		{name: "locked", status: domain.StatusLocked},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "must-not-issue"}}
			svc, repo := newServiceWithIssuer(t, stub)
			user := seedInactiveUserWithHash(t, repo, "cp-"+tc.name, "oldpass", tc.status)
			beforeHash := user.PasswordHash
			beforePV := user.PasswordVersion

			_, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
				UserID:      user.ID,
				OldPassword: "oldpass",
				NewPassword: "newpass",
			})

			// 403 ERR_AUTH_USER_NOT_ACTIVE, gate fires before mutation.
			require.Error(t, err)
			var ce *errcode.Error
			require.True(t, errors.As(err, &ce), "expected *errcode.Error, got %T", err)
			assert.Equal(t, errcode.ErrAuthUserNotActive, ce.Code)

			// Old hash NOT rewritten + version unchanged (UpdatePassword not committed).
			after, gerr := repo.GetByIDInTenant(context.Background(), testTenantID, user.ID)
			require.NoError(t, gerr)
			assert.Equal(t, beforeHash, after.PasswordHash,
				"inactive account password hash must be unchanged")
			assert.NoError(t, bcrypt.CompareHashAndPassword(
				[]byte(after.PasswordHash), []byte("oldpass"),
			),
				"stored hash must still match the old password")
			assert.Equal(t, beforePV, after.PasswordVersion,
				"passwordVersion must not advance when gate rejects")

			// IssueForUser must not run after a pre-mutation rejection.
			assert.Equal(t, int64(0), stub.calls.Load(),
				"IssueForUser must not be called when inactive gate rejects pre-mutation")
		})
	}
}

// freezeAfterReadRepo models the #1017 F1 concurrent-freeze window: a
// Lock/Suspend commits right after changePasswordInTx's non-locking GetByID
// (which returns the stale active snapshot) and before the write. UpdatePassword's
// `status='active'` predicate must then reject, so the now-frozen account's
// credential is not rewritten.
type freezeAfterReadRepo struct {
	ports.UserRepository
	target string
	froze  bool
}

// GetByIDInTenant implements the freeze-after-read spy for the changePasswordInTx
// path which uses GetByIDInTenant (F2). Freeze-after-read semantics:
// the caller gets a stale active snapshot and the write guard rejects.
func (r *freezeAfterReadRepo) GetByIDInTenant(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	u, err := r.UserRepository.GetByIDInTenant(ctx, t, id)
	if err == nil && id == r.target && !r.froze {
		r.froze = true
		_ = r.UpdateLockState(ctx, t, id, domain.StatusLocked, time.Now())
	}
	return u, err
}

// TestService_ChangePassword_ConcurrentFreeze_RejectedAtWriteGuard pins #1017 F1:
// when a freeze commits between the (non-locking) read and the write, the
// write-time status guard in UpdatePassword rejects with ErrAuthUserNotActive
// and the credential is not rewritten. The read-time Assert gate saw the stale
// active snapshot and passed — the write guard is the backstop.
func TestService_ChangePassword_ConcurrentFreeze_RejectedAtWriteGuard(t *testing.T) {
	memRepo := mem.NewStore(clock.Real()).UserRepository()
	user := seedUserWithHash(t, memRepo, "cp-freeze", "oldpass", false) // active at seed
	spy := &freezeAfterReadRepo{UserRepository: memRepo, target: user.ID}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "must-not-issue"}}
	svc, err := NewService(clock.Real(), spy,
		newInvalidator(t, memRepo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(stub), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	beforeHash := user.PasswordHash
	beforePV := user.PasswordVersion

	_, cpErr := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      user.ID,
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})

	// Write-time status guard rejects the freeze-during-tx window.
	require.Error(t, cpErr)
	var ce *errcode.Error
	require.True(t, errors.As(cpErr, &ce), "expected *errcode.Error, got %T", cpErr)
	assert.Equal(t, errcode.ErrAuthUserNotActive, ce.Code)

	after, gerr := memRepo.GetByIDInTenant(context.Background(), testTenantID, user.ID)
	require.NoError(t, gerr)
	assert.Equal(t, beforeHash, after.PasswordHash,
		"credential must not be rewritten when a freeze committed before the write")
	assert.Equal(t, beforePV, after.PasswordVersion,
		"passwordVersion must not advance when the write guard rejects")
	assert.Equal(t, int64(0), stub.calls.Load(),
		"IssueForUser must not run when the write guard rejects")
}

func TestService_ChangePassword_NewPasswordSameAsOld(t *testing.T) {
	stub := &stubTokenIssuer{}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cp-same", "samepass", false)

	_, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-same",
		OldPassword: "samepass",
		NewPassword: "samepass",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must differ")
}

// TestService_ChangePassword_BcryptError tests that ChangePassword propagates
// errors from the hash generation step. We simulate this by supplying a
// new password that is pathologically long (bcrypt rejects inputs > 72 bytes
// with a cost > MinCost in some versions, but the reliable path is to rely on
// the existing VerifyOldPasswordFail coverage for the bcrypt verify step and
// instead assert the wrong-old-password path leaves the hash unchanged).
// The original nil-issuer path is gone: NewService now rejects nil tokenIssuer
// at construction time (see TestNewService_RequiresTokenIssuer).
func TestService_ChangePassword_IssuerAlwaysInvoked(t *testing.T) {
	// Confirm that a service with a working issuer returns a real pair,
	// proving the issuer is always invoked (no nil short-circuit path remains).
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "at", RefreshToken: "rt"}}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cp-issuer-required", "oldpass", false)

	pair, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-issuer-required",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken, "tokenIssuer is always wired; pair must never be zero-value on success")
	assert.Equal(t, "at", pair.AccessToken)
}

func TestService_ChangePassword_ClearsResetFlag(t *testing.T) {
	stub := &stubTokenIssuer{pair: dto.TokenPair{}}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cp-reset", "oldpass", true)

	_, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-reset",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)

	updated, _ := repo.GetByIDInTenant(context.Background(), testTenantID, "usr-cp-reset")
	assert.False(t, updated.PasswordResetRequired(), "flag must be cleared after password change")
}

func TestService_ChangePassword_IssuerError(t *testing.T) {
	issuerErr := errors.New("token sign failure")
	stub := &stubTokenIssuer{err: issuerErr}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cp-issuer-err", "oldpass", false)

	_, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-issuer-err",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "issue token")
}

// TestService_ChangePassword_RevokesPriorSessions verifies F2 session
// convergence: after a successful password change, all pre-existing sessions
// are revoked so a stolen refresh token cannot keep minting new access tokens.
// The freshly issued replacement pair is not itself revoked.
func TestService_ChangePassword_RevokesPriorSessions(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "new-at", SessionID: "sess-new"}}
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(stub), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	seedUserWithHash(t, userRepo, "cp-revoke", "oldpass", false)

	// Seed two active sessions for this user.
	for i, sid := range []string{"sess-old-1", "sess-old-2"} {
		sess := &session.Session{
			ID:                sid,
			SubjectID:         "usr-cp-revoke",
			JTI:               fmt.Sprintf("jti-cp-revoke-%d", i),
			AuthzEpochAtIssue: 1,
			ExpiresAt:         time.Now().Add(time.Hour),
			CreatedAt:         time.Now(),
		}
		require.NoError(t, sessionRepo.Create(context.Background(), testTenantID, sess))
	}

	_, err = svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-revoke",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)

	for _, sid := range []string{"sess-old-1", "sess-old-2"} {
		got, gerr := sessionRepo.Get(context.Background(), sid)
		require.NoError(t, gerr)
		assert.True(t, got.RevokedAt != nil,
			"session %s must be revoked after ChangePassword (fail-closed on stolen refresh)", sid)
	}
}

// revokeFailingSessionStore wraps session.Store and fails RevokeForSubject
// with a fixed error — exercises the F10 transactional boundary:
// RevokeForSubject failure must abort ChangePassword before any new token is issued.
type revokeFailingSessionStore struct {
	session.Store
	err error
}

func (r *revokeFailingSessionStore) RevokeForSubject(
	_ context.Context, _ string, _ session.CredentialEvent, _ credentialfence.FenceToken,
) error {
	return r.err
}

// snapshotTxRunner is a TxRunner test double that mimics commit/rollback
// semantics on a UserRepository: it snapshots the user state before fn and
// restores on fn error so tests can assert the password write was rolled back.
// NoopTxRunner cannot exercise this because mem repos commit immediately.
type snapshotTxRunner struct {
	repo   *mem.UserRepository
	userID string
}

func (s *snapshotTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	pre, getErr := s.repo.GetByIDInTenant(ctx, testTenantID, s.userID)
	if getErr != nil {
		return fn(ctx)
	}
	if err := fn(ctx); err != nil {
		// Restore the password snapshot — equivalent to PG ROLLBACK on the user row.
		// UpdatePassword uses CAS on version; after fn ran UpdatePassword, the version
		// advanced to pre.PasswordVersion+1, so the restore call uses that as expectedPV.
		_, _ = s.repo.UpdatePassword(ctx, testTenantID, pre.ID, pre.PasswordHash, pre.PasswordResetRequired(), pre.PasswordVersion+1)
		return err
	}
	return nil
}

// TestService_ChangePassword_RevokeFailureAbortsAndNoToken verifies the F10
// transaction boundary: if the session revoke step fails inside the tx, the
// call must (a) return an error, (b) NOT invoke the token issuer, AND (c) NOT
// commit the password change. (a)+(b) prevent handing the client a fresh
// TokenPair while stolen refresh tokens stay live; (c) ensures the password
// has not been silently rotated to a value the user doesn't know.
//
// snapshotTxRunner mimics PG commit/rollback semantics on top of mem repos so
// the rollback assertion is meaningful. NoopTxRunner would commit immediately
// and leave the password mutated even after fn error — which is the exact
// PG-mode failure mode this test exists to forbid.
func TestService_ChangePassword_RevokeFailureAbortsAndNoToken(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := &revokeFailingSessionStore{
		Store: testutil.RealSessionRepo(t),
		err:   errors.New("transient DB error"),
	}
	refreshStore := newIdentityRefreshStore()
	issuerCalled := false
	stub := &stubTokenIssuer{
		pair: dto.TokenPair{AccessToken: "must-not-see"},
	}
	spyIssuer := &recordingTokenIssuer{inner: stub, called: &issuerCalled}
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(spyIssuer),
		WithTxManager(persistence.WrapForCell(&snapshotTxRunner{repo: userRepo, userID: "usr-cp-tx-fail"})))
	require.NoError(t, err)

	seedUserWithHash(t, userRepo, "cp-tx-fail", "oldpass", false)

	pair, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cp-tx-fail",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken, "zero-value pair must be returned on error")
	assert.Contains(t, err.Error(), "revoke sessions",
		"error must propagate from the transactional fn, not the token issuer")
	assert.False(t, issuerCalled,
		"token issuer must not run after tx failure: otherwise stolen refresh tokens stay live while a fresh pair is handed out")

	// (c) password rollback: the old password must still verify.
	persisted, perr := userRepo.GetByIDInTenant(context.Background(), testTenantID, "usr-cp-tx-fail")
	require.NoError(t, perr)
	assert.NoError(t,
		bcrypt.CompareHashAndPassword([]byte(persisted.PasswordHash), []byte("oldpass")),
		"password must remain on the old hash after revoke failure (tx rollback)")
	assert.Error(t,
		bcrypt.CompareHashAndPassword([]byte(persisted.PasswordHash), []byte("newpass")),
		"new password must not be persisted after revoke failure")
}

// recordingTokenIssuer records whether IssueForUser was invoked.
type recordingTokenIssuer struct {
	inner  TokenIssuer
	called *bool
}

func (r *recordingTokenIssuer) IssueForUser(ctx context.Context, userID string) (dto.TokenPair, error) {
	*r.called = true
	return r.inner.IssueForUser(ctx, userID)
}

// ---------------------------------------------------------------------------
// CAS ChangePassword tests (S6 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01)
// ---------------------------------------------------------------------------

// TestChangePassword_OldPasswordCorrect_BumpsVersion verifies that a
// successful password change increments the stored PasswordVersion so the
// CAS guard advances monotonically.
func TestChangePassword_OldPasswordCorrect_BumpsVersion(t *testing.T) {
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "at-v1", RefreshToken: "rt-v1"}}
	svc, repo := newServiceWithIssuer(t, stub)
	seedUserWithHash(t, repo, "cas-bump", "oldpass", false)

	_, err := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cas-bump",
		OldPassword: "oldpass",
		NewPassword: "newpass1",
	})
	require.NoError(t, err)

	got, err := repo.GetByIDInTenant(context.Background(), testTenantID, "usr-cas-bump")
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.PasswordVersion,
		"PasswordVersion must advance to 1 after the first successful change")
}

// TestChangePassword_StalePasswordVersion_ReturnsConflict verifies that a
// second concurrent ChangePassword carrying the original (now stale)
// password_version is rejected with ErrVersionConflict (HTTP 409).
func TestChangePassword_StalePasswordVersion_ReturnsConflict(t *testing.T) {
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "at", RefreshToken: "rt"}}
	repo := mem.NewStore(clock.Real()).UserRepository()

	// Create the user with a known bcrypt hash.
	hash, err := bcrypt.GenerateFromPassword([]byte("oldpass"), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := domain.NewUser("cas-stale", "cas-stale@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-cas-stale"
	user.PasswordVersion = 0
	require.NoError(t, repo.Create(context.Background(), testTenantID, user))

	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(stub), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	// First change: succeeds and bumps version to 1.
	_, err = svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
		UserID:      "usr-cas-stale",
		OldPassword: "oldpass",
		NewPassword: "newpass1",
	})
	require.NoError(t, err)

	// Second change: uses old password and stale version (0). The mem repo
	// will reject because version is now 1.
	// We need a stub that returns the old user with version=0 to simulate the
	// stale-read scenario. We test the repo directly here.
	_, err = repo.UpdatePassword(context.Background(), testTenantID, "usr-cas-stale", "$2a$12$stub", false, 0)
	require.Error(t, err, "stale version must yield an error")
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindConflict, ce.Kind)
	assert.Equal(t, errcode.ErrVersionConflict, ce.Code)
}

// TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds is the live
// regression for the mem tx lock-ownership fix (archtest
// MEM-TX-LOCK-OWNERSHIP-01; ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md).
//
// It deliberately wires simpleTxRunner — a pass-through runner that holds no
// lock (the exact shape that, under the old bool sentinel, made repo methods
// skip locking with no lock held and produced `fatal error: concurrent map
// writes` (the PR #552 CI flake)). With the sealed lock-witness, a foreign
// runner forces every repo method onto its per-call store.mu, so 8 goroutines
// mutating the same user can never race the maps.
//
// What this test guards (race-safe invariants — NOT timing-dependent):
//
//   - never `fatal error: concurrent map writes` / DATA RACE under -race
//     (the lock-ownership fix itself);
//   - exactly one ChangePassword succeeds (exactly-one-success invariant);
//   - all (N-1) losers fail with a *legitimate* race outcome. Per-call
//     locking does NOT give cross-method atomicity (GetByID → bcrypt →
//     UpdatePassword are three independently-locked steps), so each loser
//     is either ErrVersionConflict (its GetByID snapshotted version 0 before
//     the winner committed → CAS rejects) OR ErrAuthOldPasswordIncorrect
//     (its GetByID ran after the winner committed → reads the new hash,
//     bcrypt of the old password fails). Both are correct under the mem
//     model; asserting only ErrVersionConflict would itself be a latent flake.
//   - the stored password_version advances to exactly 1 (one mutation total).
//
// Concurrency: 8 goroutines (up from 2 to increase race window coverage).
//
// The STRONG exactly-once CAS-conflict property under true concurrency (both
// txs read version 0, exactly one gets ErrVersionConflict) is a real-MVCC
// property and is covered by the real-DB counterpart
// TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds_PG
// (service_pg_integration_test.go, //go:build integration). The mem store's
// only concurrency primitive is the all-or-nothing store.mu; it cannot model
// "two txs both read v0 then one CAS-fails" without cross-method atomicity,
// and that is by design — see the ADR.
func TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds(t *testing.T) {
	t.Parallel()

	const concurrency = 8

	hash, err := bcrypt.GenerateFromPassword([]byte("oldpass"), bcrypt.MinCost)
	require.NoError(t, err)

	repo := mem.NewStore(clock.Real()).UserRepository()
	user, err := domain.NewUser("cas-race", "cas-race@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-cas-race"
	user.PasswordVersion = 0
	require.NoError(t, repo.Create(context.Background(), testTenantID, user))

	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "at", RefreshToken: "rt"}}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(stub), WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	type result struct{ err error }
	results := make(chan result, concurrency)

	for i := 0; i < concurrency; i++ {
		newPw := fmt.Sprintf("newpass%d", i+1)
		go func(newPw string) {
			_, cerr := svc.ChangePassword(withTenant(auth.TestContext("test-self", nil)), ChangePasswordInput{
				UserID:      "usr-cas-race",
				OldPassword: "oldpass",
				NewPassword: newPw,
			})
			results <- result{cerr}
		}(newPw)
	}

	var (
		successes  int
		raceLosers int
	)
	for i := 0; i < concurrency; i++ {
		r := <-results
		if r.err == nil {
			successes++
			continue
		}
		var ce *errcode.Error
		require.ErrorAs(t, r.err, &ce,
			"concurrent ChangePassword loser must be a classified *errcode.Error, got %v", r.err)
		// Both are legitimate per-call-locked race outcomes; see godoc.
		require.Containsf(t,
			[]errcode.Code{errcode.ErrVersionConflict, errcode.ErrAuthOldPasswordIncorrect},
			ce.Code,
			"concurrent ChangePassword loser must be ErrVersionConflict or "+
				"ErrAuthOldPasswordIncorrect, got %s", ce.Code)
		raceLosers++
	}
	assert.Equal(t, 1, successes, "exactly one concurrent ChangePassword must succeed")
	assert.Equal(t, concurrency-1, raceLosers,
		"exactly %d concurrent ChangePassword goroutines must lose the race", concurrency-1)

	// Exactly one mutation applied — version advances to exactly 1.
	got, err := repo.GetByIDInTenant(context.Background(), testTenantID, "usr-cas-race")
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.PasswordVersion, "version must be exactly 1 after exactly one success")
}

// ---------------------------------------------------------------------------
// Create RequirePasswordReset tests
// ---------------------------------------------------------------------------

func TestService_Create_RequirePasswordResetTrue_UserMarked(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username:             "req-reset",
		Email:                "r@r.com",
		Password:             "pass",
		RequirePasswordReset: true,
	})
	require.NoError(t, err)
	assert.True(t, user.PasswordResetRequired(), "user must have PasswordResetRequired set when input flag is true")
}

func TestService_Create_DefaultFalse(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "no-reset",
		Email:    "n@n.com",
		Password: "pass",
	})
	require.NoError(t, err)
	assert.False(t, user.PasswordResetRequired(), "default user must not have PasswordResetRequired set")
}

// ---------------------------------------------------------------------------
// Update RequirePasswordReset tests
// ---------------------------------------------------------------------------

func TestService_Update_SetRequirePasswordResetTrue(t *testing.T) {
	svc, repo := newServiceWithIssuer(t, nil)
	seedUserWithHash(t, repo, "upd-flag-true", "pass", false)

	flagTrue := true
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:                   "usr-upd-flag-true",
		RequirePasswordReset: &flagTrue,
	})
	require.NoError(t, err)
	assert.True(t, updated.PasswordResetRequired())
}

func TestService_Update_ClearRequirePasswordReset(t *testing.T) {
	svc, repo := newServiceWithIssuer(t, nil)
	seedUserWithHash(t, repo, "upd-flag-clear", "pass", true) // starts with flag=true

	flagFalse := false
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:                   "usr-upd-flag-clear",
		RequirePasswordReset: &flagFalse,
	})
	require.NoError(t, err)
	assert.False(t, updated.PasswordResetRequired())
}

func TestService_Update_OmittedFieldNoChange(t *testing.T) {
	svc, repo := newServiceWithIssuer(t, nil)
	seedUserWithHash(t, repo, "upd-flag-omit", "pass", true) // starts with flag=true

	// Update only email, leave RequirePasswordReset nil → no change.
	newEmail := domain.NonEmpty("new@omit.com")
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:    "usr-upd-flag-omit",
		Email: &newEmail,
	})
	require.NoError(t, err)
	assert.True(t, updated.PasswordResetRequired(), "omitted field must not change existing flag")
	assert.Equal(t, "new@omit.com", updated.Email)
}

// ---------------------------------------------------------------------------
// A1: resolveCredentialMutation combined-authz-fields conflict detection
// ---------------------------------------------------------------------------

// TestService_Update_CombinedAuthzFields_Rejected is the RED→GREEN table-driven
// test for A1: a PATCH that provides both status and requirePasswordReset in a
// single request must be rejected deterministically with HTTP 400
// (KindInvalid / ErrAuthIdentityInvalidInput). Pre-fix the call silently
// discarded requirePasswordReset; post-fix both are rejected.
func TestService_Update_CombinedAuthzFields_Rejected(t *testing.T) {
	trueVal := true
	falseVal := false
	suspended := "suspended"
	active := "active"

	tests := []struct {
		name                 string
		status               *string
		requirePasswordReset *bool
	}{
		{
			name:                 "suspended+require=true",
			status:               &suspended,
			requirePasswordReset: &trueVal,
		},
		{
			name:                 "suspended+require=false",
			status:               &suspended,
			requirePasswordReset: &falseVal,
		},
		{
			name:                 "active+require=true",
			status:               &active,
			requirePasswordReset: &trueVal,
		},
		{
			name:                 "active+require=false",
			status:               &active,
			requirePasswordReset: &falseVal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newServiceWithIssuer(t, nil)
			seedUserWithHash(t, repo, "combined-authz-"+tt.name, "pass", false)
			userID := "usr-combined-authz-" + tt.name

			_, err := svc.Update(adminCtxForService(), UpdateInput{
				ID:                   userID,
				Status:               tt.status,
				RequirePasswordReset: tt.requirePasswordReset,
			})
			require.Error(t, err, "combined status+requirePasswordReset must be rejected")
			var ce *errcode.Error
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, errcode.KindInvalid, ce.Kind, "must be HTTP 400")
			assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ce.Code)
			assert.Contains(t, ce.Message, "cannot both be set")
		})
	}
}

// TestService_Update_StatusAlone_NotRejected asserts that providing only status
// (no requirePasswordReset) still succeeds — confirming the guard is additive
// and does not break the happy path.
func TestService_Update_StatusAlone_NotRejected(t *testing.T) {
	svc, repo := newServiceWithIssuer(t, nil)
	seedUserWithHash(t, repo, "status-alone", "pass", false)

	suspended := "suspended"
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:     "usr-status-alone",
		Status: &suspended,
	})
	require.NoError(t, err, "status-only update must still succeed")
	assert.Equal(t, domain.StatusSuspended, updated.Status())
}

// TestService_Update_RequirePasswordResetAlone_NotRejected asserts that
// providing only requirePasswordReset (no status) still succeeds.
func TestService_Update_RequirePasswordResetAlone_NotRejected(t *testing.T) {
	svc, repo := newServiceWithIssuer(t, nil)
	seedUserWithHash(t, repo, "flag-alone", "pass", false)

	trueVal := true
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:                   "usr-flag-alone",
		RequirePasswordReset: &trueVal,
	})
	require.NoError(t, err, "requirePasswordReset-only update must still succeed")
	assert.True(t, updated.PasswordResetRequired())
}

// failingPublisher returns an error on every Publish call, used to drive the
// publisher-error warn-log branch in Service.publish (demo mode).
type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

// TestService_Create_PublishError_DoesNotFailCreate verifies that demo-mode
// publisher failure in Service.publish is logged but does not propagate as an
// error — covering the else-branch warn log introduced when the direct publish
// path was wrapped in a v1 envelope (P1-14 follow-up).
func TestService_Create_PublishError_DoesNotFailCreate(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	fp := failingPublisher{err: errors.New("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "accesscore",
		outbox.WithLogger(slog.Default()),
	)
	require.NoError(t, err)
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionRepo, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(emitter)), WithTokenIssuer(&stubTokenIssuer{}),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "pub-err-user", Email: "pub@err.com", Password: "hash",
	})
	require.NoError(t, err, "publish failure in demo mode must not fail Create")
	assert.NotEmpty(t, user.ID)
}

// ---------------------------------------------------------------------------
// PR-CFG-H — Lock/Unlock atomicity (audit S-3) +
//             Create blank-input validation (audit S-4)
// ---------------------------------------------------------------------------

// recordingTxRunner observes whether the wrapped repository call happened
// inside RunInTx. inTx is true only between RunInTx invocation and the
// closure's return. runs counts how many times RunInTx was invoked.
// It holds no lock, so repo methods take their per-call lock (race-safe,
// no cross-method atomicity).
type recordingTxRunner struct {
	inTx bool
	runs int
}

func (r *recordingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.runs++
	r.inTx = true
	defer func() { r.inTx = false }()
	return fn(ctx)
}

// observingUserRepo snapshots `runner.inTx` at the moment GetByID / Update
// fire so a test can assert read-modify-write atomicity. Embedding the
// ports.UserRepository interface satisfies the contract for unobserved
// methods (GetByUsername / Delete).
type observingUserRepo struct {
	ports.UserRepository
	runner      *recordingTxRunner
	getInTx     bool
	updInTx     bool
	createCalls int
}

func (r *observingUserRepo) Create(ctx context.Context, t tenant.TenantID, user *domain.User) error {
	r.createCalls++
	return r.UserRepository.Create(ctx, t, user)
}

func (r *observingUserRepo) GetByIDInTenant(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	r.getInTx = r.runner.inTx
	return r.UserRepository.GetByIDInTenant(ctx, t, id)
}

func (r *observingUserRepo) GetByIDForUpdate(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	r.getInTx = r.runner.inTx
	return r.UserRepository.GetByIDForUpdate(ctx, t, id)
}

func (r *observingUserRepo) UpdateLockState(
	ctx context.Context, t tenant.TenantID, userID string, status domain.UserStatus, now time.Time,
) error {
	r.updInTx = r.runner.inTx
	return r.UserRepository.UpdateLockState(ctx, t, userID, status, now)
}

func (r *observingUserRepo) UpdateProfile(
	ctx context.Context, t tenant.TenantID, userID string,
	name, email *domain.NonEmpty, now time.Time,
) (*domain.User, error) {
	r.updInTx = r.runner.inTx
	return r.UserRepository.UpdateProfile(ctx, t, userID, name, email, now)
}

func (r *observingUserRepo) UpdatePasswordResetFlag(
	ctx context.Context, t tenant.TenantID, userID string, required bool, now time.Time,
) error {
	r.updInTx = r.runner.inTx
	return r.UserRepository.UpdatePasswordResetFlag(ctx, t, userID, required, now)
}

// failingUpdateRepo wraps a real repo but always fails UpdateLockState — used
// to drive the Unlock-error-propagation test. GetByID is forwarded so the
// service's read step succeeds.
type failingUpdateRepo struct {
	ports.UserRepository
	updateErr error
	updates   int
}

func (r *failingUpdateRepo) UpdateLockState(_ context.Context, _ tenant.TenantID, _ string, _ domain.UserStatus, _ time.Time) error {
	r.updates++
	return r.updateErr
}

// newAtomicitySvc wires Service with a recordingTxRunner + observingUserRepo
// so atomicity tests can inspect inTx state without touching production wiring.
func newAtomicitySvc(t *testing.T) (*Service, *observingUserRepo, *recordingTxRunner) {
	t.Helper()
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewStore(clock.Real()).UserRepository(), runner: runner}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(runner)))
	require.NoError(t, err)
	return svc, repo, runner
}

// TestService_Lock_GetByIDAndUpdateInsideTx asserts the read-modify-write
// chain in Lock executes inside one RunInTx, closing the TOCTOU window where
// a concurrent transaction could mutate the user between the read and the
// write (audit S-3).
func TestService_Lock_GetByIDAndUpdateInsideTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "lock-atomic", Email: "l@a.t", Password: "hash",
	})
	require.NoError(t, err)
	// Reset observation state so the Lock call is captured in isolation
	// (Create itself runs inside RunInTx and would otherwise be conflated).
	repo.getInTx, repo.updInTx, runner.runs = false, false, 0

	require.NoError(t, svc.Lock(adminCtxForService(), user.ID))
	// Wave 5 P1-1: Lock now runs 2 txs: (1) last-admin guard, (2) ApplyInTx+publish
	// co-committed in the same RunInTx (L2 OutboxFact guarantee).
	assert.Equal(t, 2, runner.runs, "Lock must run 2 txs: guard + (ApplyInTx+publish co-committed)")
	assert.True(t, repo.getInTx, "Lock.GetByID/GetByIDForUpdate must be observed inside RunInTx (no TOCTOU window)")
	assert.True(t, repo.updInTx, "Lock.Update must run inside the authzmutate tx")
}

// TestService_Update_GetByIDAndUpdateInsideTx asserts Update's read-modify-
// write chain runs inside a single RunInTx, closing the same TOCTOU window
// pattern as Lock/Unlock (audit S-3 same-pattern, reviewer F7). Pre-fix
// Update had no tx wrapping; post-fix both repo calls observe inTx=true.
func TestService_Update_GetByIDAndUpdateInsideTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "update-atomic", Email: "u@p.t", Password: "hash",
	})
	require.NoError(t, err)
	repo.getInTx, repo.updInTx, runner.runs = false, false, 0

	newEmail := domain.NonEmpty("new@p.t")
	updated, err := svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err)
	assert.Equal(t, "new@p.t", updated.Email)
	assert.Equal(t, 1, runner.runs, "Update must run inside exactly one tx")
	assert.True(t, repo.getInTx, "Update.GetByID must be observed inside RunInTx (no TOCTOU window)")
	assert.True(t, repo.updInTx, "Update.Update must run inside the same tx")
}

// TestService_GetByID_RunsInsideTx (PR-3b review F1, Site 1) asserts GetByID
// reads the user inside a RunInTx so the RLS app.tenant_id GUC is injected from
// the authenticated principal's ctxkeys.TenantID. users is under FORCE ROW LEVEL
// SECURITY (migration 053); a bare-pool read would be fail-closed to 0 rows
// under the restricted app-serving pool (#1676). GetByID was the lone
// identitymanage read that bypassed the tx wrapping every other method uses.
func TestService_GetByID_RunsInsideTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "get-scoped", Email: "g@s.t", Password: "hash",
	})
	require.NoError(t, err)
	repo.getInTx, runner.runs = false, 0

	got, err := svc.GetByID(adminCtxForService(), user.ID)
	require.NoError(t, err)
	assert.Equal(t, user.ID, got.ID)
	assert.Equal(t, 1, runner.runs, "GetByID must run inside exactly one tx")
	assert.True(t, repo.getInTx, "GetByID must read inside RunInTx so the RLS GUC is set (F1)")
}

// TestService_Update_InvalidStatusFailsBeforeTx asserts that the cheap
// status string validation rejects invalid values before opening a tx —
// invalid input is not a database concern.
func TestService_Update_InvalidStatusFailsBeforeTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "upd-bad-status", Email: "b@s.t", Password: "hash",
	})
	require.NoError(t, err)
	repo.getInTx, repo.updInTx, runner.runs = false, false, 0

	bad := "deleted"
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Status: &bad})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
	assert.Equal(t, 0, runner.runs, "invalid status must be rejected before opening a tx")
}

// TestService_Unlock_UpdateInsideTx asserts Unlock's write (UpdateLockState)
// executes inside the RunInTx closure — Wave 5 P1-1: ApplyInTx+publish
// co-commit in a single RunInTx (1 tx total, L2 OutboxFact guarantee).
// Note: Unlock no longer calls GetByID inside the tx; the narrow UpdateLockState
// port method performs a direct targeted write — there is no preliminary read.
func TestService_Unlock_UpdateInsideTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "unlock-atomic", Email: "u@a.t", Password: "hash",
	})
	require.NoError(t, err)
	require.NoError(t, svc.Lock(adminCtxForService(), user.ID))
	repo.getInTx, repo.updInTx, runner.runs = false, false, 0

	require.NoError(t, svc.Unlock(adminCtxForService(), user.ID))
	// Wave 5 P1-1: ApplyInTx+publish co-committed in the same RunInTx → 1 tx.
	assert.Equal(t, 1, runner.runs, "Unlock must run 1 tx: ApplyInTx+publish co-committed")
	assert.True(t, repo.updInTx, "Unlock.UpdateLockState must run inside the authzmutate tx")
	assert.False(t, repo.getInTx, "Unlock must not call GetByID inside tx (narrow UpdateLockState does self-lookup)")
}

// TestService_Unlock_UpdateErrorPropagatesAndAbortsBeforeLog asserts that an
// Update failure inside Unlock's tx returns a wrapped error and prevents the
// success log line from running. The error must wrap "identity-manage:
// unlock:" so the call site is identifiable.
func TestService_Unlock_UpdateErrorPropagatesAndAbortsBeforeLog(t *testing.T) {
	innerRepo := mem.NewStore(clock.Real()).UserRepository()
	user, err := domain.NewUser("rb", "rb@e.t", "hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-rb"
	user.SetStatus(domain.StatusLocked, time.Now())
	require.NoError(t, innerRepo.Create(context.Background(), testTenantID, user))

	failRepo := &failingUpdateRepo{UserRepository: innerRepo, updateErr: errors.New("disk full")}
	runner := &recordingTxRunner{}
	sessionStore2 := testutil.RealSessionRepo(t)
	refreshStore2 := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), failRepo, newInvalidator(t, failRepo, sessionStore2, refreshStore2), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(runner)))
	require.NoError(t, err)

	err = svc.Unlock(adminCtxForService(), "usr-rb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity-manage: unlock:",
		"error must wrap with the unlock call-site prefix")
	assert.Contains(t, err.Error(), "disk full", "underlying error must be unwrapable")
	assert.Equal(t, 1, runner.runs, "RunInTx must have been invoked exactly once")
	assert.Equal(t, 1, failRepo.updates, "Update was attempted inside the tx")
}

// TestService_Create_BlankUsername_RejectsBeforeRepoCreate asserts that a
// blank username is caught by validation.RequireNotEmpty with the typed
// invalid-input code, and that no expensive work (bcrypt, repo.Create) runs
// (audit S-4).
func TestService_Create_BlankUsername_RejectsBeforeRepoCreate(t *testing.T) {
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewStore(clock.Real()).UserRepository(), runner: runner}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(runner)))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "", Email: "ok@e.t", Password: "pw",
	})
	require.Error(t, err)
	assert.Nil(t, user)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code,
		"blank username must be rejected with the identity-invalid-input code")
	assert.Equal(t, "validation: required field missing", ec.Message)
	var gotField string
	for _, attr := range ec.Details {
		if attr.Key() == "field" {
			s, ok := attr.Value().(string)
			require.True(t, ok, "expected string for 'field' detail, got %T", attr.Value())
			gotField = s
			break
		}
	}
	assert.Equal(t, "username", gotField, "details must carry the field name")
	assert.Equal(t, 0, repo.createCalls, "repo.Create must not run when input is blank")
	assert.Equal(t, 0, runner.runs, "RunInTx must not run when input validation fails")
}

// TestService_Create_BlankEmail_RejectsBeforeRepoCreate is the email-blank
// twin of TestService_Create_BlankUsername_RejectsBeforeRepoCreate.
func TestService_Create_BlankEmail_RejectsBeforeRepoCreate(t *testing.T) {
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewStore(clock.Real()).UserRepository(), runner: runner}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(runner)))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "ok", Email: "", Password: "pw",
	})
	require.Error(t, err)
	assert.Nil(t, user)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
	assert.Equal(t, "validation: required field missing", ec.Message)
	var gotFieldEmail string
	for _, attr := range ec.Details {
		if attr.Key() == "field" {
			s, ok := attr.Value().(string)
			require.True(t, ok, "expected string for 'field' detail, got %T", attr.Value())
			gotFieldEmail = s
			break
		}
	}
	assert.Equal(t, "email", gotFieldEmail, "details must carry the field name")
	assert.Equal(t, 0, repo.createCalls)
	assert.Equal(t, 0, runner.runs)
}

// TestService_Create_BlankPassword_RoutesIdentityInvalidInputCode covers the
// third RequireNotEmpty field. Pre-fix this path was already wired
// (password was the sole field), so the case is a regression guard ensuring
// the error code stays bound to the service boundary
// (ErrAuthIdentityInvalidInput) and never leaks domain.NewUser's
// ErrAuthInvalidInput.
func TestService_Create_BlankPassword_RoutesIdentityInvalidInputCode(t *testing.T) {
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewStore(clock.Real()).UserRepository(), runner: runner}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), repo, newInvalidator(t, repo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(runner)))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "ok", Email: "ok@e.t", Password: "",
	})
	require.Error(t, err)
	assert.Nil(t, user)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
	assert.Equal(t, "validation: required field missing", ec.Message)
	var gotFieldPwd string
	for _, attr := range ec.Details {
		if attr.Key() == "field" {
			s, ok := attr.Value().(string)
			require.True(t, ok, "expected string for 'field' detail, got %T", attr.Value())
			gotFieldPwd = s
			break
		}
	}
	assert.Equal(t, "password", gotFieldPwd, "details must carry the field name")
	assert.Equal(t, 0, repo.createCalls)
	assert.Equal(t, 0, runner.runs)
}

// TestService_Create_RequireNotEmptyShortCircuitsOnFirstField asserts the
// validator returns on the FIRST blank field in declaration order
// (username → email → password), matching setup.CreateAdmin's order so the
// two paths produce identical messages for identical inputs. Asserts both
// the typed error code (stable contract) and the field-name message
// (debuggability).
func TestService_Create_RequireNotEmptyShortCircuitsOnFirstField(t *testing.T) {
	svc := newTestService(t)
	_, err := svc.Create(adminCtxForService(), CreateInput{
		Username: "", Email: "", Password: "",
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code,
		"blank-input rejection must route the typed identity-invalid-input code regardless of which field short-circuits")
	assert.Equal(t, "validation: required field missing", ec.Message,
		"validator must short-circuit on the first declared field; setup.CreateAdmin uses the same order")
	var gotFieldSC string
	for _, attr := range ec.Details {
		if attr.Key() == "field" {
			s, ok := attr.Value().(string)
			require.True(t, ok, "expected string for 'field' detail, got %T", attr.Value())
			gotFieldSC = s
			break
		}
	}
	assert.Equal(t, "username", gotFieldSC, "validator must short-circuit on username (first declared)")
}

// ---------------------------------------------------------------------------
// PR-CFG-H — Lock failure-injection coverage (review six-role P2-1)
// ---------------------------------------------------------------------------

// spyEmitter implements outbox.Emitter, counting calls and returning a fixed
// error so tests can assert publish-path failure handling. err == nil makes
// it pure-counter spy.
type spyEmitter struct {
	err   error
	calls int
}

func (e *spyEmitter) Emit(_ context.Context, _ outbox.Entry) error {
	e.calls++
	return e.err
}

// failingRefreshStore wraps a real refresh.Store and forces RevokeUser to
// return a fixed error, exercising Lock's refresh-revoke failure branch
// (review P2-1).
type failingRefreshStore struct {
	refresh.Store
	revokeUserErr error
	calls         int
}

func (s *failingRefreshStore) RevokeUser(_ context.Context, _ string, _ credentialfence.FenceToken) error {
	s.calls++
	return s.revokeUserErr
}

// TestService_Lock_RefreshRevokeFailureAbortsBeforePublishAndLog asserts the
// Lock transaction aborts on refresh-store revoke failure: the error wraps
// the call-site prefix, the outbox publish does not run (would commit a
// "user.locked" event for a still-unrevoked refresh chain), and the success
// log line does not fire (would mislead operators).
func TestService_Lock_RefreshRevokeFailureAbortsBeforePublishAndLog(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	domainUser, err := domain.NewUser("rf-fail", "rf@e.t", "hash", time.Now())
	require.NoError(t, err)
	domainUser.ID = "usr-rf-fail"
	require.NoError(t, userRepo.Create(context.Background(), testTenantID, domainUser))

	failRefresh := &failingRefreshStore{
		Store:         newIdentityRefreshStore(),
		revokeUserErr: errors.New("refresh DB unavailable"),
	}
	emitter := &spyEmitter{}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	sessionStore := testutil.RealSessionRepo(t)
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, sessionStore, failRefresh), logger,
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(emitter)), WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	err = svc.Lock(adminCtxForService(), "usr-rf-fail")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "credentialinvalidate: revoke refresh chain",
		"error must contain the invalidator refresh-revoke prefix")
	assert.Contains(t, err.Error(), "refresh DB unavailable", "underlying error must be unwrapable")
	assert.Equal(t, 1, failRefresh.calls, "refresh.RevokeUser was attempted once")
	assert.Equal(t, 0, emitter.calls,
		"publish must not run after refresh-revoke failure:"+
			" tx must abort first or a TopicUserLocked event"+
			" would commit while the refresh chain stays live")
	assert.Nil(t, sloghelper.FindLogEntry(buf.String(), "user locked"),
		"success log line must not fire when the tx aborts")
}

// ---------------------------------------------------------------------------
// F4: capturingEmitter — typed payload assertions for Lock/Unlock/Update/Delete
// ---------------------------------------------------------------------------

// capturingEmitter records every emitted Entry for typed-payload assertions.
type capturingEmitter struct {
	entries []outbox.Entry
}

func (c *capturingEmitter) Emit(_ context.Context, entry outbox.Entry) error {
	c.entries = append(c.entries, entry)
	return nil
}

// TestService_Lock_EmitsTypedPayload asserts that Lock emits exactly one
// event.user.locked.v1 with a non-empty userId and actorId.
func TestService_Lock_EmitsTypedPayload(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "lock-payload", Email: "lp@e.t", Password: "hash"})
	require.NoError(t, err)

	cap := &capturingEmitter{}
	emitUserRepo := mem.NewStore(clock.Real()).UserRepository()
	emitSessionStore := testutil.RealSessionRepo(t)
	emitRefreshStore := newIdentityRefreshStore()
	svc2, err := NewService(clock.Real(), emitUserRepo, newInvalidator(t, emitUserRepo, emitSessionStore, emitRefreshStore), slog.Default(),
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(cap)), WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)
	// Create user in svc2's own repo.
	user2, err := svc2.Create(adminCtxForService(), CreateInput{Username: user.Username + "2", Email: "lp2@e.t", Password: "hash"})
	require.NoError(t, err)

	// Reset capture after Create (which also emits).
	cap.entries = nil

	require.NoError(t, svc2.Lock(adminCtxForService(), user2.ID))
	require.Len(t, cap.entries, 1, "Lock must emit exactly one event")

	var payload dto.UserLockedEvent
	require.NoError(t, json.Unmarshal(cap.entries[0].Payload(), &payload))
	assert.NotEmpty(t, payload.UserID, "emitted UserLockedEvent.userId must be non-empty")
	assert.NotEmpty(t, payload.ActorID, "emitted UserLockedEvent.actorId must be non-empty")
	assert.Equal(t, user2.ID, payload.UserID)
	assert.Equal(t, "test-admin", payload.ActorID)
}

// TestService_Update_EmitsTypedPayload asserts Update emits one UserUpdatedEvent
// with non-empty userId and actorId.
func TestService_Update_EmitsTypedPayload(t *testing.T) {
	cap := &capturingEmitter{}
	updateUserRepo := mem.NewStore(clock.Real()).UserRepository()
	updateSessionStore := testutil.RealSessionRepo(t)
	updateRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(
		clock.Real(),
		updateUserRepo,
		newInvalidator(t, updateUserRepo, updateSessionStore, updateRefreshStore),
		slog.Default(),
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(cap)),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})),
	)
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "upd-payload", Email: "up@e.t", Password: "hash"})
	require.NoError(t, err)
	cap.entries = nil

	newEmail := domain.NonEmpty("upd-new@e.t")
	_, err = svc.Update(adminCtxForService(), UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err)
	require.Len(t, cap.entries, 1, "Update must emit exactly one event")

	var payload dto.UserUpdatedEvent
	require.NoError(t, json.Unmarshal(cap.entries[0].Payload(), &payload))
	assert.NotEmpty(t, payload.UserID, "emitted UserUpdatedEvent.userId must be non-empty")
	assert.NotEmpty(t, payload.ActorID, "emitted UserUpdatedEvent.actorId must be non-empty")
	assert.Equal(t, user.ID, payload.UserID)
	assert.Equal(t, "test-admin", payload.ActorID)
}

// TestService_Delete_EmitsTypedPayload asserts Delete emits one UserDeletedEvent
// with non-empty userId and actorId.
func TestService_Delete_EmitsTypedPayload(t *testing.T) {
	cap := &capturingEmitter{}
	delUserRepo := mem.NewStore(clock.Real()).UserRepository()
	delSessionStore := testutil.RealSessionRepo(t)
	delRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), delUserRepo, newInvalidator(t, delUserRepo, delSessionStore, delRefreshStore), slog.Default(),
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(cap)), WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "del-payload", Email: "dp@e.t", Password: "hash"})
	require.NoError(t, err)
	cap.entries = nil

	require.NoError(t, svc.Delete(adminCtxForService(), user.ID))
	require.Len(t, cap.entries, 1, "Delete must emit exactly one event")

	var payload dto.UserDeletedEvent
	require.NoError(t, json.Unmarshal(cap.entries[0].Payload(), &payload))
	assert.NotEmpty(t, payload.UserID, "emitted UserDeletedEvent.userId must be non-empty")
	assert.NotEmpty(t, payload.ActorID, "emitted UserDeletedEvent.actorId must be non-empty")
	assert.Equal(t, user.ID, payload.UserID)
	assert.Equal(t, "test-admin", payload.ActorID)
}

// TestService_Unlock_EmitsTypedPayload asserts Unlock emits one UserUnlockedEvent
// with non-empty userId and actorId.
func TestService_Unlock_EmitsTypedPayload(t *testing.T) {
	cap := &capturingEmitter{}
	unlockUserRepo := mem.NewStore(clock.Real()).UserRepository()
	unlockSessionStore := testutil.RealSessionRepo(t)
	unlockRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(
		clock.Real(),
		unlockUserRepo,
		newInvalidator(t, unlockUserRepo, unlockSessionStore, unlockRefreshStore),
		slog.Default(),
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(cap)),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})),
	)
	require.NoError(t, err)

	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "unl-payload", Email: "ulp@e.t", Password: "hash"})
	require.NoError(t, err)
	require.NoError(t, svc.Lock(adminCtxForService(), user.ID))
	cap.entries = nil

	require.NoError(t, svc.Unlock(adminCtxForService(), user.ID))
	require.Len(t, cap.entries, 1, "Unlock must emit exactly one event")

	var payload dto.UserUnlockedEvent
	require.NoError(t, json.Unmarshal(cap.entries[0].Payload(), &payload))
	assert.NotEmpty(t, payload.UserID, "emitted UserUnlockedEvent.userId must be non-empty")
	assert.NotEmpty(t, payload.ActorID, "emitted UserUnlockedEvent.actorId must be non-empty")
	assert.Equal(t, user.ID, payload.UserID)
	assert.Equal(t, "test-admin", payload.ActorID)
}

// TestService_Lock_NoActor_ReturnsUnauthorized asserts that Lock with a
// context that carries no auth principal returns ErrAuthUnauthorized.
// This guards the actorFromContext fail-fast invariant.
func TestService_Lock_NoActor_ReturnsUnauthorized(t *testing.T) {
	svc := newTestService(t)
	user, err := svc.Create(adminCtxForService(), CreateInput{Username: "lock-noauth", Email: "na@e.t", Password: "hash"})
	require.NoError(t, err)

	// context.Background() has no auth principal.
	err = svc.Lock(context.Background(), user.ID)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ec.Code,
		"Lock without auth context must return ErrAuthUnauthorized")
}

// ---------------------------------------------------------------------------
// F2: resolveCredentialMutationFromUser — in-tx idempotency check (no pre-tx GetByID)
// ---------------------------------------------------------------------------

// countingUserRepo wraps a real repo and counts GetByIDInTenant calls so tests can
// assert that no extra GetByIDInTenant is issued outside the transaction for the
// RequirePasswordReset idempotency check (F2 fix).
type countingUserRepo struct {
	ports.UserRepository
	getByIDCalls int
}

func (r *countingUserRepo) GetByIDInTenant(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	r.getByIDCalls++
	return r.UserRepository.GetByIDInTenant(ctx, t, id)
}

// TestService_Update_RequirePasswordReset_NoExtraGetByIDOutsideTx verifies
// that when RequirePasswordReset=true is sent for a user that does NOT already
// have the flag set, Update issues exactly ONE GetByID call (inside tx1) and
// no pre-tx GetByID (F2 TOCTOU fix).
//
// Before the F2 fix, resolveCredentialMutation called s.repo.GetByID(ctx, id)
// outside the tx for the idempotency check, then tx1 called GetByID again
// inside the tx — two total calls. After the fix only the in-tx call remains.
func TestService_Update_RequirePasswordReset_NoExtraGetByIDOutsideTx(t *testing.T) {
	t.Parallel()
	inner := mem.NewStore(clock.Real()).UserRepository()
	cRepo := &countingUserRepo{UserRepository: inner}
	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), cRepo, newInvalidator(t, cRepo, sessionStore, refreshStore), slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	// Seed a user without the flag set.
	seedUserWithHash(t, inner, "f2-no-extra-get", "pw", false)
	cRepo.getByIDCalls = 0 // reset after seed

	trueVal := true
	_, err = svc.Update(adminCtxForService(), UpdateInput{
		ID:                   "usr-f2-no-extra-get",
		RequirePasswordReset: &trueVal,
	})
	require.NoError(t, err)
	// The in-tx GetByID is 1 call (from tx1). authzmutate.Apply does its own
	// GetByIDForUpdate (not counted here), plus re-fetch after mutation = 1 more.
	// Neither is the pre-tx extra call that F2 eliminates.
	// Key invariant: no GetByID is called BEFORE the first RunInTx (i.e., none
	// at the top of applyUserUpdate before the tx opens). We cannot easily
	// distinguish in-tx from out-of-tx here without the observingUserRepo, but
	// the count is bounded by: 1 (tx1 GetByID) + 1 (re-fetch after mutation) = 2.
	// Pre-F2 the count was 3 (pre-tx GetByID + tx1 GetByID + re-fetch).
	assert.LessOrEqual(t, cRepo.getByIDCalls, 2,
		"F2: Update(requirePasswordReset=true) must issue at most 2 GetByID calls "+
			"(in-tx + re-fetch); pre-tx extra GetByID was eliminated")
}

// TestService_Update_RequirePasswordReset_AlreadySet_NoMutation verifies that
// when RequirePasswordReset=true is sent for a user that already has the flag
// set, the idempotency check (now reading from the tx-locked row) correctly
// returns no-op — no authzmutate.Apply is invoked, so authz_epoch is not bumped.
func TestService_Update_RequirePasswordReset_AlreadySet_NoMutation(t *testing.T) {
	t.Parallel()
	svc, repo := newServiceWithIssuer(t, nil)
	// Seed with flag already true.
	seedUserWithHash(t, repo, "f2-already-set", "pw", true)

	trueVal := true
	updated, err := svc.Update(adminCtxForService(), UpdateInput{
		ID:                   "usr-f2-already-set",
		RequirePasswordReset: &trueVal,
	})
	require.NoError(t, err, "already-set idempotent call must succeed")
	assert.True(t, updated.PasswordResetRequired(),
		"flag must remain true after no-op idempotent call")
	// Epoch should not have changed (no invalidation triggered).
	after, err := repo.GetByIDInTenant(context.Background(), testTenantID, "usr-f2-already-set")
	require.NoError(t, err)
	assert.Equal(t, int64(1), after.AuthzEpoch(),
		"F2: idempotent RequirePasswordReset=true must NOT bump authz_epoch "+
			"when flag is already set (no spurious session invalidation)")
}

// TestService_Lock_PublishFailureAbortsBeforeLog asserts the success log
// does not fire when the outbox publish itself fails: the failed publish is
// the last step inside the tx, so a missing log line is the operator-visible
// signal that the lock was not durably announced.
func TestService_Lock_PublishFailureAbortsBeforeLog(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	domainUser, err := domain.NewUser("pub-fail", "pub@e.t", "hash", time.Now())
	require.NoError(t, err)
	domainUser.ID = "usr-pub-fail"
	require.NoError(t, userRepo.Create(context.Background(), testTenantID, domainUser))

	emitter := &spyEmitter{err: errors.New("broker unavailable")}
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	pubSessionStore := testutil.RealSessionRepo(t)
	pubRefreshStore := newIdentityRefreshStore()
	svc, err := NewService(clock.Real(), userRepo, newInvalidator(t, userRepo, pubSessionStore, pubRefreshStore), logger,
		inertRoleRepo(),
		WithEmitter(outbox.WrapEmitterForCell(emitter)), WithTokenIssuer(minimalStubIssuer),
		WithTxManager(persistence.WrapForCell(simpleTxRunner{})))
	require.NoError(t, err)

	err = svc.Lock(adminCtxForService(), "usr-pub-fail")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker unavailable", "underlying publish error must be unwrapable")
	assert.Equal(t, 1, emitter.calls, "publish was attempted exactly once for TopicUserLocked")
	assert.Nil(t, sloghelper.FindLogEntry(buf.String(), "user locked"),
		"success log line must not fire when the tx publish step fails")
}
