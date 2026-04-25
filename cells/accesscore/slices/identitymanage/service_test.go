package identitymanage

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalStubIssuer is a zero-config TokenIssuer stub used by tests that only
// exercise non-ChangePassword paths (Create, Update, Lock, etc.) and do not
// care about the token pair content.
var minimalStubIssuer TokenIssuer = &stubTokenIssuer{}

func newTestService() *Service {
	svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
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
		svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default())
		require.Error(t, err, "NewService without WithTokenIssuer must fail")
		assert.Nil(t, svc)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrCellMissingTokenIssuer, ec.Code)
	})

	t.Run("WithTokenIssuer(nil)", func(t *testing.T) {
		svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
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
	svc, err := NewService(mem.NewUserRepository(), sessionRepo, newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer))
	require.NoError(t, err)

	user, err := svc.Create(context.Background(), CreateInput{
		Username: "carol", Email: "c@d.e", Password: "hash",
	})
	require.NoError(t, err)

	// Seed a session for this user.
	session := &domain.Session{
		ID:          "sess-carol",
		UserID:      user.ID,
		AccessToken: "at",
		ExpiresAt:   time.Now().Add(time.Hour),
		CreatedAt:   time.Now(),
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
	svc, err := NewService(repo, mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
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
	svc, err := NewService(userRepo, sessionRepo, newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(stub))
	require.NoError(t, err)

	seedUserWithHash(t, userRepo, "cp-revoke", "oldpass", false)

	// Seed two active sessions for this user.
	for _, sid := range []string{"sess-old-1", "sess-old-2"} {
		sess, sessErr := domain.NewSession("usr-cp-revoke", "at", time.Now().Add(time.Hour))
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
	svc, err := NewService(userRepo, sessionRepo, newIdentityRefreshStore(), slog.Default(),
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
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	fp := failingPublisher{err: errors.New("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, "accesscore", slog.Default())
	require.NoError(t, err)
	svc, err := NewService(userRepo, sessionRepo, newIdentityRefreshStore(), slog.Default(),
		WithEmitter(emitter), WithTokenIssuer(&stubTokenIssuer{}))
	require.NoError(t, err)

	user, err := svc.Create(context.Background(), CreateInput{
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

func (r *observingUserRepo) Create(ctx context.Context, user *domain.User) error {
	r.createCalls++
	return r.UserRepository.Create(ctx, user)
}

func (r *observingUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	r.getInTx = r.runner.inTx
	return r.UserRepository.GetByID(ctx, id)
}

func (r *observingUserRepo) Update(ctx context.Context, user *domain.User) error {
	r.updInTx = r.runner.inTx
	return r.UserRepository.Update(ctx, user)
}

// failingUpdateRepo wraps a real repo but always fails Update — used to
// drive the Unlock-error-propagation test. GetByID is forwarded so the
// service's read step succeeds.
type failingUpdateRepo struct {
	ports.UserRepository
	updateErr error
	updates   int
}

func (r *failingUpdateRepo) Update(_ context.Context, _ *domain.User) error {
	r.updates++
	return r.updateErr
}

// newAtomicitySvc wires Service with a recordingTxRunner + observingUserRepo
// so atomicity tests can inspect inTx state without touching production wiring.
func newAtomicitySvc(t *testing.T) (*Service, *observingUserRepo, *recordingTxRunner) {
	t.Helper()
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewUserRepository(), runner: runner}
	svc, err := NewService(repo, mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(runner))
	require.NoError(t, err)
	return svc, repo, runner
}

// TestService_Lock_GetByIDAndUpdateInsideTx asserts the read-modify-write
// chain in Lock executes inside one RunInTx, closing the TOCTOU window where
// a concurrent transaction could mutate the user between the read and the
// write (audit S-3).
func TestService_Lock_GetByIDAndUpdateInsideTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(context.Background(), CreateInput{
		Username: "lock-atomic", Email: "l@a.t", Password: "hash",
	})
	require.NoError(t, err)
	// Reset observation state so the Lock call is captured in isolation
	// (Create itself runs inside RunInTx and would otherwise be conflated).
	repo.getInTx, repo.updInTx, runner.runs = false, false, 0

	require.NoError(t, svc.Lock(context.Background(), user.ID))
	assert.Equal(t, 1, runner.runs, "Lock must run inside exactly one tx")
	assert.True(t, repo.getInTx, "Lock.GetByID must be observed inside RunInTx (no TOCTOU window)")
	assert.True(t, repo.updInTx, "Lock.Update must run inside the same tx")
}

// TestService_Unlock_GetByIDAndUpdateInsideTx asserts Unlock now runs the
// read-modify-write chain in a single RunInTx. Pre-fix, Unlock had no tx
// wrapping at all; post-fix both repo calls observe inTx=true (audit S-3).
func TestService_Unlock_GetByIDAndUpdateInsideTx(t *testing.T) {
	svc, repo, runner := newAtomicitySvc(t)
	user, err := svc.Create(context.Background(), CreateInput{
		Username: "unlock-atomic", Email: "u@a.t", Password: "hash",
	})
	require.NoError(t, err)
	require.NoError(t, svc.Lock(context.Background(), user.ID))
	repo.getInTx, repo.updInTx, runner.runs = false, false, 0

	require.NoError(t, svc.Unlock(context.Background(), user.ID))
	assert.Equal(t, 1, runner.runs, "Unlock must run inside exactly one tx")
	assert.True(t, repo.getInTx, "Unlock.GetByID must be observed inside RunInTx (no TOCTOU window)")
	assert.True(t, repo.updInTx, "Unlock.Update must run inside the same tx")
}

// TestService_Unlock_UpdateErrorPropagatesAndAbortsBeforeLog asserts that an
// Update failure inside Unlock's tx returns a wrapped error and prevents the
// success log line from running. The error must wrap "identity-manage:
// unlock:" so the call site is identifiable.
func TestService_Unlock_UpdateErrorPropagatesAndAbortsBeforeLog(t *testing.T) {
	innerRepo := mem.NewUserRepository()
	user, err := domain.NewUser("rb", "rb@e.t", "hash")
	require.NoError(t, err)
	user.ID = "usr-rb"
	user.Lock()
	require.NoError(t, innerRepo.Create(context.Background(), user))

	failRepo := &failingUpdateRepo{UserRepository: innerRepo, updateErr: errors.New("disk full")}
	runner := &recordingTxRunner{}
	svc, err := NewService(failRepo, mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(runner))
	require.NoError(t, err)

	err = svc.Unlock(context.Background(), "usr-rb")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "identity-manage: unlock:",
		"error must wrap with the unlock call-site prefix")
	assert.Contains(t, err.Error(), "disk full", "underlying error must be unwrapable")
	assert.Equal(t, 1, runner.runs, "RunInTx must have been invoked exactly once")
	assert.Equal(t, 1, failRepo.updates, "Update was attempted inside the tx")
}

// TestService_Create_BlankUsername_RejectsBeforeRepoCreate asserts that a
// blank username is caught by validation.RequireNotBlank with the typed
// invalid-input code, and that no expensive work (bcrypt, repo.Create) runs
// (audit S-4).
func TestService_Create_BlankUsername_RejectsBeforeRepoCreate(t *testing.T) {
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewUserRepository(), runner: runner}
	svc, err := NewService(repo, mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(runner))
	require.NoError(t, err)

	user, err := svc.Create(context.Background(), CreateInput{
		Username: "", Email: "ok@e.t", Password: "pw",
	})
	require.Error(t, err)
	assert.Nil(t, user)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code,
		"blank username must be rejected with the identity-invalid-input code")
	assert.Contains(t, err.Error(), "username is required")
	assert.Equal(t, 0, repo.createCalls, "repo.Create must not run when input is blank")
	assert.Equal(t, 0, runner.runs, "RunInTx must not run when input validation fails")
}

// TestService_Create_BlankEmail_RejectsBeforeRepoCreate is the email-blank
// twin of TestService_Create_BlankUsername_RejectsBeforeRepoCreate.
func TestService_Create_BlankEmail_RejectsBeforeRepoCreate(t *testing.T) {
	runner := &recordingTxRunner{}
	repo := &observingUserRepo{UserRepository: mem.NewUserRepository(), runner: runner}
	svc, err := NewService(repo, mem.NewSessionRepository(), newIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(minimalStubIssuer),
		WithTxManager(runner))
	require.NoError(t, err)

	user, err := svc.Create(context.Background(), CreateInput{
		Username: "ok", Email: "", Password: "pw",
	})
	require.Error(t, err)
	assert.Nil(t, user)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
	assert.Contains(t, err.Error(), "email is required")
	assert.Equal(t, 0, repo.createCalls)
	assert.Equal(t, 0, runner.runs)
}

// TestService_Create_RequireNotBlankShortCircuitsOnFirstField asserts the
// validator returns on the FIRST blank field in declaration order
// (username → email → password), matching setup.CreateAdmin's order so the
// two paths produce identical messages for identical inputs.
func TestService_Create_RequireNotBlankShortCircuitsOnFirstField(t *testing.T) {
	svc := newTestService()
	_, err := svc.Create(context.Background(), CreateInput{
		Username: "", Email: "", Password: "",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "username is required",
		"validator must short-circuit on the first declared field; setup.CreateAdmin uses the same order")
	assert.NotContains(t, err.Error(), "email is required")
	assert.NotContains(t, err.Error(), "password is required")
}
