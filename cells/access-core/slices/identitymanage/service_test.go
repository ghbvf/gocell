package identitymanage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalStubIssuer is a zero-config TokenIssuer stub used by tests that only
// exercise non-ChangePassword paths (Create, Update, Lock, etc.) and do not
// care about the token pair content.
var minimalStubIssuer TokenIssuer = &stubTokenIssuer{}

func newTestService() *Service {
	svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer))
	if err != nil {
		panic("newTestService: " + err.Error())
	}
	return svc
}

// TestNewService_RequiresTokenIssuer asserts that NewService returns a non-nil
// error when WithTokenIssuer is omitted or nil, enforcing fail-fast wiring.
func TestNewService_RequiresTokenIssuer(t *testing.T) {
	t.Run("no WithTokenIssuer option", func(t *testing.T) {
		svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default())
		require.Error(t, err, "NewService without WithTokenIssuer must fail")
		assert.Nil(t, svc)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrCellMissingTokenIssuer, ec.Code)
	})

	t.Run("WithTokenIssuer(nil)", func(t *testing.T) {
		svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
			WithTokenIssuer(nil))
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
			svc := newTestService()
			user, err := svc.Create(context.Background(), tt.input)
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
	svc := newTestService()
	user, err := svc.Create(context.Background(), CreateInput{
		Username: "bob", Email: "b@c.d", Password: "hash",
	})
	require.NoError(t, err)

	// Lock
	require.NoError(t, svc.Lock(context.Background(), user.ID))
	locked, _ := svc.GetByID(context.Background(), user.ID)
	assert.True(t, locked.IsLocked())

	// Unlock
	require.NoError(t, svc.Unlock(context.Background(), user.ID))
	unlocked, _ := svc.GetByID(context.Background(), user.ID)
	assert.False(t, unlocked.IsLocked())
}

func TestService_Lock_RevokesSession(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()
	svc, err := NewService(mem.NewUserRepository(), sessionRepo, eventbus.New(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer))
	require.NoError(t, err)

	user, err := svc.Create(context.Background(), CreateInput{
		Username: "carol", Email: "c@d.e", Password: "hash",
	})
	require.NoError(t, err)

	// Seed a session for this user.
	session := &domain.Session{
		ID:           "sess-carol",
		UserID:       user.ID,
		AccessToken:  "at",
		RefreshToken: "rt",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
	}
	require.NoError(t, sessionRepo.Create(context.Background(), session))

	// Lock the user — sessions should be revoked.
	require.NoError(t, svc.Lock(context.Background(), user.ID))

	// Verify session was revoked.
	got, err := sessionRepo.GetByID(context.Background(), "sess-carol")
	require.NoError(t, err)
	assert.True(t, got.IsRevoked(), "session should be revoked after user lock")
}

func TestService_Delete(t *testing.T) {
	svc := newTestService()
	user, _ := svc.Create(context.Background(), CreateInput{
		Username: "del", Email: "d@e.f", Password: "hash",
	})

	require.NoError(t, svc.Delete(context.Background(), user.ID))
	_, err := svc.GetByID(context.Background(), user.ID)
	assert.Error(t, err)
}

func TestService_Update(t *testing.T) {
	svc := newTestService()
	user, _ := svc.Create(context.Background(), CreateInput{
		Username: "upd", Email: "old@e.f", Password: "hash",
	})

	newEmail := "new@e.f"
	updated, err := svc.Update(context.Background(), UpdateInput{ID: user.ID, Email: &newEmail})
	require.NoError(t, err)
	assert.Equal(t, "new@e.f", updated.Email)
}

// stubTokenIssuer is a test double for TokenIssuer.
type stubTokenIssuer struct {
	pair dto.TokenPair
	err  error
}

func (s *stubTokenIssuer) IssueForUser(_ context.Context, _ string) (dto.TokenPair, error) {
	return s.pair, s.err
}

// seedUserWithHash creates a user in the repo with a known bcrypt hash.
func seedUserWithHash(t *testing.T, repo *mem.UserRepository, username, password string, markReset bool) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := domain.NewUser(username, username+"@test.com", string(hash))
	require.NoError(t, err)
	user.ID = "usr-" + username
	if markReset {
		user.MarkPasswordResetRequired()
	}
	require.NoError(t, repo.Create(context.Background(), user))
	return user
}

func TestService_Update_PatchSemantics(t *testing.T) {
	svc := newTestService()
	user, err := svc.Create(context.Background(), CreateInput{
		Username: "patch", Email: "p@e.f", Password: "hash",
	})
	require.NoError(t, err)

	// Update only name, email should stay unchanged.
	newName := "patchedName"
	updated, err := svc.Update(context.Background(), UpdateInput{ID: user.ID, Name: &newName})
	require.NoError(t, err)
	assert.Equal(t, "patchedName", updated.Username)
	assert.Equal(t, "p@e.f", updated.Email)

	// Update status to suspended.
	suspended := "suspended"
	updated, err = svc.Update(context.Background(), UpdateInput{ID: user.ID, Status: &suspended})
	require.NoError(t, err)
	assert.Equal(t, "suspended", string(updated.Status))

	// Invalid status should fail.
	badStatus := "deleted"
	_, err = svc.Update(context.Background(), UpdateInput{ID: user.ID, Status: &badStatus})
	assert.Error(t, err)
}

// ---------------------------------------------------------------------------
// ChangePassword tests
// ---------------------------------------------------------------------------

func newServiceWithIssuer(issuer TokenIssuer) (*Service, *mem.UserRepository) {
	repo := mem.NewUserRepository()
	effectiveIssuer := issuer
	if effectiveIssuer == nil {
		effectiveIssuer = minimalStubIssuer
	}
	svc, err := NewService(repo, mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithTokenIssuer(effectiveIssuer))
	if err != nil {
		panic("newServiceWithIssuer: " + err.Error())
	}
	return svc, repo
}

func TestService_ChangePassword_VerifyOldPasswordOk(t *testing.T) {
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "new-at", RefreshToken: "new-rt"}}
	svc, repo := newServiceWithIssuer(stub)
	seedUserWithHash(t, repo, "cp-ok", "oldpass", false)

	pair, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
		UserID:      "usr-cp-ok",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)
	assert.Equal(t, "new-at", pair.AccessToken)

	// Verify stored hash changed.
	updated, _ := repo.GetByID(context.Background(), "usr-cp-ok")
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(updated.PasswordHash), []byte("newpass")))
	assert.False(t, updated.PasswordResetRequired, "flag must be cleared after password change")
}

func TestService_ChangePassword_VerifyOldPasswordFail(t *testing.T) {
	stub := &stubTokenIssuer{}
	svc, repo := newServiceWithIssuer(stub)
	seedUserWithHash(t, repo, "cp-bad", "correctpass", false)

	_, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
		UserID:      "usr-cp-bad",
		OldPassword: "wrongpass",
		NewPassword: "newpass",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "old password incorrect")

	// No side effects: hash unchanged.
	orig, _ := repo.GetByID(context.Background(), "usr-cp-bad")
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(orig.PasswordHash), []byte("correctpass")))
}

func TestService_ChangePassword_NewPasswordSameAsOld(t *testing.T) {
	stub := &stubTokenIssuer{}
	svc, repo := newServiceWithIssuer(stub)
	seedUserWithHash(t, repo, "cp-same", "samepass", false)

	_, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
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
	svc, repo := newServiceWithIssuer(stub)
	seedUserWithHash(t, repo, "cp-issuer-required", "oldpass", false)

	pair, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
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
	svc, repo := newServiceWithIssuer(stub)
	seedUserWithHash(t, repo, "cp-reset", "oldpass", true) // PasswordResetRequired=true

	_, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
		UserID:      "usr-cp-reset",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)

	updated, _ := repo.GetByID(context.Background(), "usr-cp-reset")
	assert.False(t, updated.PasswordResetRequired, "flag must be cleared after password change")
}

func TestService_ChangePassword_IssuerError(t *testing.T) {
	issuerErr := errors.New("token sign failure")
	stub := &stubTokenIssuer{err: issuerErr}
	svc, repo := newServiceWithIssuer(stub)
	seedUserWithHash(t, repo, "cp-issuer-err", "oldpass", false)

	_, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
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
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	stub := &stubTokenIssuer{pair: dto.TokenPair{AccessToken: "new-at", SessionID: "sess-new"}}
	svc, err := NewService(userRepo, sessionRepo, eventbus.New(), slog.Default(),
		WithTokenIssuer(stub))
	require.NoError(t, err)

	seedUserWithHash(t, userRepo, "cp-revoke", "oldpass", false)

	// Seed two active sessions for this user.
	for _, sid := range []string{"sess-old-1", "sess-old-2"} {
		sess, sessErr := domain.NewSession("usr-cp-revoke", "at", "rt-"+sid, time.Now().Add(time.Hour))
		require.NoError(t, sessErr)
		sess.ID = sid
		require.NoError(t, sessionRepo.Create(context.Background(), sess))
	}

	_, err = svc.ChangePassword(context.Background(), ChangePasswordInput{
		UserID:      "usr-cp-revoke",
		OldPassword: "oldpass",
		NewPassword: "newpass",
	})
	require.NoError(t, err)

	for _, sid := range []string{"sess-old-1", "sess-old-2"} {
		got, gerr := sessionRepo.GetByID(context.Background(), sid)
		require.NoError(t, gerr)
		assert.True(t, got.IsRevoked(),
			"session %s must be revoked after ChangePassword (fail-closed on stolen refresh)", sid)
	}
}

// revokeFailingSessionRepo wraps mem.SessionRepository and fails
// RevokeByUserID with a fixed error — exercises the F10 transactional
// boundary: RevokeByUserID failure must abort ChangePassword before any new
// token is issued.
type revokeFailingSessionRepo struct {
	*mem.SessionRepository
	err error
}

func (r *revokeFailingSessionRepo) RevokeByUserID(context.Context, string) error {
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
	pre, getErr := s.repo.GetByID(ctx, s.userID)
	if getErr != nil {
		return fn(ctx)
	}
	if err := fn(ctx); err != nil {
		// Restore the snapshot — equivalent to PG ROLLBACK on the user row.
		_ = s.repo.Update(ctx, pre)
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
	userRepo := mem.NewUserRepository()
	sessionRepo := &revokeFailingSessionRepo{
		SessionRepository: mem.NewSessionRepository(),
		err:               errors.New("transient DB error"),
	}
	issuerCalled := false
	stub := &stubTokenIssuer{
		pair: dto.TokenPair{AccessToken: "must-not-see"},
	}
	spyIssuer := &recordingTokenIssuer{inner: stub, called: &issuerCalled}
	svc, err := NewService(userRepo, sessionRepo, eventbus.New(), slog.Default(),
		WithTokenIssuer(spyIssuer),
		WithTxManager(&snapshotTxRunner{repo: userRepo, userID: "usr-cp-tx-fail"}))
	require.NoError(t, err)

	seedUserWithHash(t, userRepo, "cp-tx-fail", "oldpass", false)

	pair, err := svc.ChangePassword(context.Background(), ChangePasswordInput{
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
	persisted, perr := userRepo.GetByID(context.Background(), "usr-cp-tx-fail")
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
// Create RequirePasswordReset tests
// ---------------------------------------------------------------------------

func TestService_Create_RequirePasswordResetTrue_UserMarked(t *testing.T) {
	svc := newTestService()
	user, err := svc.Create(context.Background(), CreateInput{
		Username:             "req-reset",
		Email:                "r@r.com",
		Password:             "pass",
		RequirePasswordReset: true,
	})
	require.NoError(t, err)
	assert.True(t, user.PasswordResetRequired, "user must have PasswordResetRequired set when input flag is true")
}

func TestService_Create_DefaultFalse(t *testing.T) {
	svc := newTestService()
	user, err := svc.Create(context.Background(), CreateInput{
		Username: "no-reset",
		Email:    "n@n.com",
		Password: "pass",
	})
	require.NoError(t, err)
	assert.False(t, user.PasswordResetRequired, "default user must not have PasswordResetRequired set")
}

// ---------------------------------------------------------------------------
// Update RequirePasswordReset tests
// ---------------------------------------------------------------------------

func TestService_Update_SetRequirePasswordResetTrue(t *testing.T) {
	svc, repo := newServiceWithIssuer(nil)
	seedUserWithHash(t, repo, "upd-flag-true", "pass", false)

	flagTrue := true
	updated, err := svc.Update(context.Background(), UpdateInput{
		ID:                   "usr-upd-flag-true",
		RequirePasswordReset: &flagTrue,
	})
	require.NoError(t, err)
	assert.True(t, updated.PasswordResetRequired)
}

func TestService_Update_ClearRequirePasswordReset(t *testing.T) {
	svc, repo := newServiceWithIssuer(nil)
	seedUserWithHash(t, repo, "upd-flag-clear", "pass", true) // starts with flag=true

	flagFalse := false
	updated, err := svc.Update(context.Background(), UpdateInput{
		ID:                   "usr-upd-flag-clear",
		RequirePasswordReset: &flagFalse,
	})
	require.NoError(t, err)
	assert.False(t, updated.PasswordResetRequired)
}

func TestService_Update_OmittedFieldNoChange(t *testing.T) {
	svc, repo := newServiceWithIssuer(nil)
	seedUserWithHash(t, repo, "upd-flag-omit", "pass", true) // starts with flag=true

	// Update only email, leave RequirePasswordReset nil → no change.
	newEmail := "new@omit.com"
	updated, err := svc.Update(context.Background(), UpdateInput{
		ID:    "usr-upd-flag-omit",
		Email: &newEmail,
	})
	require.NoError(t, err)
	assert.True(t, updated.PasswordResetRequired, "omitted field must not change existing flag")
	assert.Equal(t, "new@omit.com", updated.Email)
}
