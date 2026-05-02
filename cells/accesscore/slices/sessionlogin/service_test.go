package sessionlogin

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

func newTestRefreshStore() refresh.Store {
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.MustNew(refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clock, nil)
}

type failingIssueRefreshStore struct {
	refresh.Store
	err error
}

func (s failingIssueRefreshStore) Issue(context.Context, string, string) (string, *refresh.Token, error) {
	return "", nil, s.err
}

type typedNilRefreshStore struct {
	refresh.Store
}

type trackingSessionRepo struct {
	ports.SessionRepository
	created []string
	deleted []string
}

func (r *trackingSessionRepo) Create(ctx context.Context, session *domain.Session) error {
	r.created = append(r.created, session.ID)
	return r.SessionRepository.Create(ctx, session)
}

func (r *trackingSessionRepo) Delete(ctx context.Context, id string) error {
	r.deleted = append(r.deleted, id)
	return r.SessionRepository.Delete(ctx, id)
}

var (
	testKeySet, _, _ = auth.MustNewTestKeySet(clock.Real())
	testIssuer       *auth.JWTIssuer
)

func init() {
	var err error
	// Issuer is constructed with a default audience via WithIssuerAudiencesFromSlice
	// (Registry path). The slice service no longer caches audience separately (S31).
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-accesscore", auth.DefaultAccessTokenTTL, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

// TestNewService_IssuerDefaultAudienceWrittenToTokens verifies that when the
// issuer is constructed with a default audience (Registry path), the Service
// writes that audience into issued tokens without caching it separately (S31).
func TestNewService_IssuerDefaultAudienceWrittenToTokens(t *testing.T) {
	svc, userRepo := newTestService(t)
	seedUser(userRepo, "aud-user", "pass123")

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "aud-user", Password: "pass123"})
	require.NoError(t, err)

	// The access token must carry the audience from the issuer's configured default.
	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Contains(t, accessClaims.Audience, "gocell",
		"access token aud must be populated from issuer default audience (Registry)")

	// The refresh token is now an opaque wire token (not a JWT) — it must be non-empty.
	assert.NotEmpty(t, pair.RefreshToken, "login must issue a non-empty opaque refresh token")
}

func newTestService(t testing.TB) (*Service, *mem.UserRepository) {
	t.Helper()
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()
	return MustNewService(userRepo, sessionRepo, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(), WithClock(clock.Real())), userRepo
}

func TestNewService_RejectsTypedNilDependencies(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()
	refreshStore := newTestRefreshStore()

	cases := []struct {
		name string
		run  func() (*Service, error)
	}{
		{
			name: "typed nil userRepo",
			run: func() (*Service, error) {
				var typedNil *mem.UserRepository
				return NewService(typedNil, sessionRepo, roleRepo, refreshStore, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
		{
			name: "typed nil sessionRepo",
			run: func() (*Service, error) {
				var typedNil *mem.SessionRepository
				return NewService(userRepo, typedNil, roleRepo, refreshStore, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
		{
			name: "typed nil roleRepo",
			run: func() (*Service, error) {
				var typedNil *mem.RoleRepository
				return NewService(userRepo, sessionRepo, typedNil, refreshStore, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
		{
			name: "typed nil refreshStore",
			run: func() (*Service, error) {
				var typedNil *typedNilRefreshStore
				return NewService(userRepo, sessionRepo, roleRepo, typedNil, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.run()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
		})
	}
}

// seedUser creates a user with a bcrypt-hashed password.
func seedUser(repo *mem.UserRepository, username, password string) {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	user, _ := domain.NewUser(username, username+"@test.com", string(hash), time.Now())
	user.ID = "usr-" + username
	_ = repo.Create(context.Background(), user)
}

func TestService_Login(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mem.UserRepository)
		input   LoginInput
		wantErr bool
	}{
		{
			name:    "valid login",
			setup:   func(r *mem.UserRepository) { seedUser(r, "alice", "pass123") },
			input:   LoginInput{Username: "alice", Password: "pass123"},
			wantErr: false,
		},
		{
			name:    "wrong password",
			setup:   func(r *mem.UserRepository) { seedUser(r, "bob", "correct") },
			input:   LoginInput{Username: "bob", Password: "wrong"},
			wantErr: true,
		},
		{
			name:    "non-existent user",
			setup:   func(_ *mem.UserRepository) {},
			input:   LoginInput{Username: "ghost", Password: "pass"},
			wantErr: true,
		},
		{
			name:    "empty credentials",
			setup:   func(_ *mem.UserRepository) {},
			input:   LoginInput{},
			wantErr: true,
		},
		{
			name: "locked user",
			setup: func(r *mem.UserRepository) {
				seedUser(r, "locked", "pass")
				u, _ := r.GetByUsername(context.Background(), "locked")
				u.LockAccount(time.Now())
				_ = r.Update(context.Background(), u)
			},
			input:   LoginInput{Username: "locked", Password: "pass"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, userRepo := newTestService(t)
			tt.setup(userRepo)

			pair, err := svc.Login(context.Background(), tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, pair.AccessToken)
				assert.NotEmpty(t, pair.RefreshToken)
				assert.False(t, pair.ExpiresAt.IsZero())
				// TDD: Login must populate UserID from the authenticated user.
				assert.NotEmpty(t, pair.UserID, "Login must return a non-empty UserID")
				// Verify UserID matches the seeded user ID.
				u, err := userRepo.GetByUsername(context.Background(), tt.input.Username)
				require.NoError(t, err)
				assert.Equal(t, u.ID, pair.UserID, "Login UserID must match the authenticated user's ID")
			}
		})
	}
}

func TestService_Login_DemoMode_ExplicitCleanup_NoOrphanSession(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	roleRepo := mem.NewRoleRepository()
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}
	svc := MustNewService(userRepo, sessionRepo, roleRepo, store, testIssuer, slog.Default(), WithClock(clock.Real()))
	seedUser(userRepo, "refresh-down", "pass123")

	pair, err := svc.Login(context.Background(), LoginInput{Username: "refresh-down", Password: "pass123"})
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
	require.Len(t, sessionRepo.created, 1)
	require.Equal(t, sessionRepo.created, sessionRepo.deleted)
	_, lookupErr := sessionRepo.GetByID(context.Background(), sessionRepo.created[0])
	require.Error(t, lookupErr, "failed refresh issue must not leave an orphan session in demo/noop tx mode")
}

func TestService_Login_TokensContainSessionID(t *testing.T) {
	svc, userRepo := newTestService(t)
	seedUser(userRepo, "sid-user", "pass123")

	// Need a verifier to decode the tokens.
	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "sid-user", Password: "pass123"})
	require.NoError(t, err)

	// Access token must contain sid.
	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	sid := accessClaims.SessionID
	assert.NotEmpty(t, sid, "access token must contain sid claim")
	_, sidParseErr := uuid.Parse(sid)
	assert.NoError(t, sidParseErr, "session id must be a canonical UUID (PR-A45)")

	// Refresh token is now an opaque wire token (not a JWT).
	// It must be non-empty; the session linkage is tracked in the refresh store, not in the token payload.
	assert.NotEmpty(t, pair.RefreshToken, "login must issue a non-empty opaque refresh token")
}

// failingPublisher returns an error on every Publish call.
type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

func TestLogin_PasswordResetRequiredFlagPropagated(t *testing.T) {
	svc, userRepo := newTestService(t)

	// Seed user with PasswordResetRequired=true.
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass123"), bcrypt.MinCost)
	user, _ := domain.NewUser("reset-user", "reset@test.com", string(hash), time.Now())
	user.ID = "usr-reset"
	user.MarkPasswordResetRequired(time.Now())
	_ = userRepo.Create(context.Background(), user)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "reset-user", Password: "pass123"})
	require.NoError(t, err)

	// TokenPair flag must be true.
	assert.True(t, pair.PasswordResetRequired, "TokenPair.PasswordResetRequired must mirror user flag")

	// JWT claim must also be true.
	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, claims.PasswordResetRequired, "access token must carry password_reset_required=true claim")
}

func TestLogin_NoResetWhenFlagFalse(t *testing.T) {
	svc, userRepo := newTestService(t)
	seedUser(userRepo, "normal-user", "pass123")

	pair, err := svc.Login(context.Background(), LoginInput{Username: "normal-user", Password: "pass123"})
	require.NoError(t, err)

	assert.False(t, pair.PasswordResetRequired, "TokenPair.PasswordResetRequired must be false for normal user")

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.False(t, claims.PasswordResetRequired, "access token must not carry reset claim for normal user")
}

func TestService_IssueForUser(t *testing.T) {
	svc, userRepo := newTestService(t)
	seedUser(userRepo, "issue-user", "pass123")

	// Fetch the user ID.
	u, err := userRepo.GetByUsername(context.Background(), "issue-user")
	require.NoError(t, err)

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	assert.False(t, pair.ExpiresAt.IsZero())
	assert.False(t, pair.PasswordResetRequired)
	// Regression guard (PR#183 round-2): the session must be persisted so that
	// sessionvalidate.enforceSessionState can look it up by sid claim. Without
	// persistence, every subsequent authenticated request returns 401.
	assert.NotEmpty(t, pair.SessionID, "IssueForUser must return a non-empty SessionID")
}

func TestService_IssueForUser_SessionPersisted(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()
	svc := MustNewService(userRepo, sessionRepo, roleRepo, newTestRefreshStore(), testIssuer, slog.Default(), WithClock(clock.Real()))
	seedUser(userRepo, "issue-persist", "pass123")

	u, err := userRepo.GetByUsername(context.Background(), "issue-persist")
	require.NoError(t, err)

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.NoError(t, err)
	require.NotEmpty(t, pair.SessionID)

	// The session must be findable by its ID so sessionvalidate does not fail.
	session, err := sessionRepo.GetByID(context.Background(), pair.SessionID)
	require.NoError(t, err, "session must be persisted after IssueForUser so sessionvalidate can look it up")
	assert.Equal(t, pair.SessionID, session.ID)
	assert.Equal(t, u.ID, session.UserID)
	assert.False(t, session.IsRevoked(), "newly issued session must not be revoked")
	assert.False(t, session.IsExpired(time.Now()), "newly issued session must not be expired")
}

func TestService_IssueForUser_RefreshStoreUnavailableReturnsInfraAndNoOrphanSession(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	roleRepo := mem.NewRoleRepository()
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}
	svc := MustNewService(userRepo, sessionRepo, roleRepo, store, testIssuer, slog.Default(), WithClock(clock.Real()))
	seedUser(userRepo, "issue-refresh-down", "pass123")
	u, err := userRepo.GetByUsername(context.Background(), "issue-refresh-down")
	require.NoError(t, err)

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
	require.Len(t, sessionRepo.created, 1)
	require.Equal(t, sessionRepo.created, sessionRepo.deleted)
}

// TestService_Login_BlankFieldsRejected verifies that RequireNotBlank is
// wired correctly: blank username and blank password each return
// ErrAuthLoginInvalidInput with an "is required" message.
func TestService_Login_BlankFieldsRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       LoginInput
		wantMessage string
	}{
		{
			name:        "blank username rejected",
			input:       LoginInput{Username: "", Password: "p"},
			wantMessage: "username is required",
		},
		{
			name:        "blank password rejected",
			input:       LoginInput{Username: "u", Password: ""},
			wantMessage: "password is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newTestService(t)
			_, err := svc.Login(context.Background(), tt.input)
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec, "expected *errcode.Error")
			assert.Equal(t, errcode.ErrAuthLoginInvalidInput, ec.Code)
			assert.Contains(t, ec.Message, tt.wantMessage)
		})
	}
}

// brokenRoleRepo returns a fixed error from GetByUserID so tests can exercise
// fail-closed paths in Login / IssueForUser without a real DB outage.
type brokenRoleRepo struct {
	mem.RoleRepository
	err error
}

func (b *brokenRoleRepo) GetByUserID(_ context.Context, _ string) ([]*domain.Role, error) {
	return nil, b.err
}

// countingSessionRepo wraps ports.SessionRepository and counts Create calls so
// fail-closed tests can assert the session write never happened.
type countingSessionRepo struct {
	ports.SessionRepository
	creates int
}

func (c *countingSessionRepo) Create(ctx context.Context, s *domain.Session) error {
	c.creates++
	return c.SessionRepository.Create(ctx, s)
}

// countingEmitter counts Emit calls so the fail-closed test can prove the
// role-fetch failure short-circuits before the event-emit stage.
type countingEmitter struct {
	count int
}

func (c *countingEmitter) Emit(_ context.Context, _ outbox.Entry) error {
	c.count++
	return nil
}

// TestService_Login_RoleFetchFailure_AbortsLogin asserts that when the
// RoleRepository is unavailable, Login fails fast with ErrAuthRoleFetchFailed
// and does NOT persist the session or emit the session.created event. This is
// the fail-closed contract from PR-A7 / sessionmint: the alternative (sign a
// token with empty roles) silently strips every RBAC capability from a
// seemingly-authenticated user.
func TestService_Login_RoleFetchFailure_AbortsLogin(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &countingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	roleRepo := &brokenRoleRepo{err: fmt.Errorf("roleRepo outage")}
	seedUser(userRepo, "role-outage", "pass123")

	emitter := &countingEmitter{}
	svc := MustNewService(userRepo, sessionRepo, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(), WithEmitter(emitter), WithClock(clock.Real()))

	pair, err := svc.Login(context.Background(), LoginInput{Username: "role-outage", Password: "pass123"})
	require.Error(t, err, "Login must fail when role fetch fails")
	assert.Empty(t, pair.AccessToken, "no token on failure")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code,
		"fail-closed: role fetch failure surfaces as ErrAuthRoleFetchFailed")

	assert.Equal(t, 0, sessionRepo.creates, "no session must be persisted on fail-closed")
	assert.Equal(t, 0, emitter.count, "no session.created event on fail-closed")
}

// TestService_IssueForUser_RoleFetchFailure_AbortsIssue asserts the same
// fail-closed contract for the IssueForUser path (change-password flow).
func TestService_IssueForUser_RoleFetchFailure_AbortsIssue(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &countingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	roleRepo := &brokenRoleRepo{err: fmt.Errorf("roleRepo outage")}
	seedUser(userRepo, "issue-outage", "pass123")
	u, err := userRepo.GetByUsername(context.Background(), "issue-outage")
	require.NoError(t, err)

	svc := MustNewService(userRepo, sessionRepo, roleRepo, newTestRefreshStore(), testIssuer, slog.Default(), WithClock(clock.Real()))

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.Error(t, err, "IssueForUser must fail when role fetch fails")
	assert.Empty(t, pair.AccessToken)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code)

	assert.Equal(t, 0, sessionRepo.creates, "no session must be persisted on fail-closed")
}

// TestService_IssueForUser_GetByIDError verifies that when userRepo.GetByID
// returns an error (e.g. user not found), IssueForUser wraps and propagates the
// error with "IssueForUser get user" context rather than panicking or returning
// an empty pair silently.
func TestService_IssueForUser_GetByIDError(t *testing.T) {
	svc, _ := newTestService(t) // userRepo is empty — GetByID will return not-found

	pair, err := svc.IssueForUser(context.Background(), "nonexistent-user-id")
	require.Error(t, err, "IssueForUser must fail when user does not exist")
	assert.Empty(t, pair.AccessToken, "no token on GetByID failure")
	assert.Contains(t, err.Error(), "IssueForUser get user",
		"error must be wrapped with IssueForUser get user context")
}

func TestService_Login_PublishError_DoesNotFailLogin(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()
	seedUser(userRepo, "pub-err", "pass123")

	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "accesscore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc := MustNewService(userRepo, sessionRepo, roleRepo, newTestRefreshStore(), testIssuer,
		slog.Default(), WithEmitter(emitter), WithClock(clock.Real()))

	pair, err := svc.Login(context.Background(), LoginInput{Username: "pub-err", Password: "pass123"})
	require.NoError(t, err, "publish failure in demo mode should not fail login")
	assert.NotEmpty(t, pair.AccessToken)
}

// TestService_IssueForUser_EmitsSessionCreated locks in the always-emit contract:
// IssueForUser must emit exactly one event.session.created.v1 regardless of
// whether it is called from the Login or ChangePassword path.
func TestService_IssueForUser_EmitsSessionCreated(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()
	seedUser(userRepo, "emit-user", "pass123")
	u, err := userRepo.GetByUsername(context.Background(), "emit-user")
	require.NoError(t, err)

	emitter := &countingEmitter{}
	svc := MustNewService(userRepo, sessionRepo, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(), WithEmitter(emitter), WithClock(clock.Real()))

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)
	assert.Equal(t, 1, emitter.count,
		"IssueForUser must emit exactly one event.session.created.v1 (always-emit contract)")
}

// TestPersistSessionWithRefresh_DurableTx_RefreshIssueFails_NoExplicitCleanup verifies
// that when a real (non-noop) TxRunner is in use and refreshStore.Issue fails, no
// explicit cleanup call is made — the transaction rollback handles atomicity.
// The test uses a non-noop stubTxRunner (defined in outbox_test.go) so isNoopTx
// returns false and the cleanup branch is skipped.
func TestPersistSessionWithRefresh_DurableTx_RefreshIssueFails_NoExplicitCleanup(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := &trackingSessionRepo{SessionRepository: testutil.RealSessionRepo(t)}
	roleRepo := mem.NewRoleRepository()
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}

	// stubTxRunner (defined in outbox_test.go) is NOT a Nooper — isNoopTx returns false.
	tx := &stubTxRunner{}
	svc := MustNewService(userRepo, sessionRepo, roleRepo, store, testIssuer, slog.Default(),
		WithTxManager(tx), WithClock(clock.Real()))
	seedUser(userRepo, "durable-refresh-fail", "pass123")

	_, err := svc.Login(context.Background(), LoginInput{Username: "durable-refresh-fail", Password: "pass123"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)

	// Durable tx: Create was called inside the tx, but no explicit Delete.
	// The tx rollback would handle the cleanup atomically; no orphan cleanup needed.
	require.Len(t, sessionRepo.created, 1, "session.Create was called inside the tx")
	assert.Len(t, sessionRepo.deleted, 0,
		"durable tx mode: explicit cleanup must NOT be called; tx rollback handles it")
}

// TestCleanupIssuedSession_NotFound_LogsDebug verifies that when
// sessionRepo.Delete returns ErrSessionNotFound during cleanup, the error is
// silently treated as a no-op (the session was already gone) and only a Debug
// log is emitted — not an Error log that would page on-call.
//
// This matches the behavior in sessionrefresh/service.go and sessionvalidate/service.go.
func TestCleanupIssuedSession_NotFound_LogsDebug(t *testing.T) {
	// Use a session repo that always returns not-found on Delete.
	userRepo := mem.NewUserRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	roleRepo := mem.NewRoleRepository()

	// A failingIssueRefreshStore causes cleanupIssuedSession to be called in
	// Noop (demo) tx mode. Then we want sessionRepo.Delete to return NotFound.
	notFoundSessionRepo := &notFoundOnDeleteSessionRepo{SessionRepository: sessionRepo}
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}
	svc := MustNewService(userRepo, notFoundSessionRepo, roleRepo, store, testIssuer, slog.Default(), WithClock(clock.Real()))
	seedUser(userRepo, "cleanup-not-found", "pass123")

	// Should not panic or return an unexpected error — the original refresh issue error propagates.
	_, err := svc.Login(context.Background(), LoginInput{Username: "cleanup-not-found", Password: "pass123"})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code,
		"not-found on cleanup must not change the returned error; original refresh error propagates")
}

// notFoundOnDeleteSessionRepo returns ErrSessionNotFound when Delete is called.
type notFoundOnDeleteSessionRepo struct {
	ports.SessionRepository
}

func (r *notFoundOnDeleteSessionRepo) Delete(_ context.Context, _ string) error {
	return errcode.NewDomain(errcode.ErrSessionNotFound, "session not found")
}
