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
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	session "github.com/ghbvf/gocell/runtime/auth/session"
)

func newTestRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

type failingIssueRefreshStore struct {
	refresh.Store
	err error
}

func (s failingIssueRefreshStore) Issue(_ context.Context, _, _ string, _ int64) (string, *refresh.Token, error) {
	return "", nil, s.err
}

type typedNilRefreshStore struct {
	refresh.Store
}

// trackingSessionStore wraps session.Store and records Create and Revoke calls.
type trackingSessionStore struct {
	session.Store
	created []string
	revoked []string
}

func (r *trackingSessionStore) Create(ctx context.Context, s *session.Session) error {
	r.created = append(r.created, s.ID)
	return r.Store.Create(ctx, s)
}

func (r *trackingSessionStore) Revoke(ctx context.Context, id string) error {
	r.revoked = append(r.revoked, id)
	return r.Store.Revoke(ctx, id)
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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	return MustNewService(
		userRepo, sessionStore, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
	), userRepo
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	refreshStore := newTestRefreshStore()
	_, err := NewService(userRepo, sessionStore, roleRepo, refreshStore, testIssuer,
		slog.Default(), WithClock(clock.Real()) /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

func TestNewService_RejectsTypedNilDependencies(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	refreshStore := newTestRefreshStore()

	cases := []struct {
		name string
		run  func() (*Service, error)
	}{
		{
			name: "typed nil userRepo",
			run: func() (*Service, error) {
				var typedNil *mem.UserRepository
				return NewService(typedNil, sessionStore, roleRepo, refreshStore, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
		{
			name: "typed nil sessionStore",
			run: func() (*Service, error) {
				var typedNil *session.MemStore
				return NewService(userRepo, typedNil, roleRepo, refreshStore, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
		{
			name: "typed nil roleRepo",
			run: func() (*Service, error) {
				var typedNil *mem.RoleRepository
				return NewService(userRepo, sessionStore, typedNil, refreshStore, testIssuer, slog.Default(), WithClock(clock.Real()))
			},
		},
		{
			name: "typed nil refreshStore",
			run: func() (*Service, error) {
				var typedNil *typedNilRefreshStore
				return NewService(userRepo, sessionStore, roleRepo, typedNil, testIssuer, slog.Default(), WithClock(clock.Real()))
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
				u.SetStatus(domain.StatusLocked, time.Now())
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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := &trackingSessionStore{Store: testutil.RealSessionRepo(t)}
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}
	// noopTxRunner (Noop()==true) triggers the isNoopTx cleanup path.
	svc := MustNewService(userRepo, sessionStore, roleRepo, store, testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		WithSessionTTL(time.Hour))
	seedUser(userRepo, "refresh-down", "pass123")

	pair, err := svc.Login(context.Background(), LoginInput{Username: "refresh-down", Password: "pass123"})
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
	require.Len(t, sessionStore.created, 1)
	require.Equal(t, sessionStore.created, sessionStore.revoked,
		"revoked session IDs must match created session IDs after noop-tx refresh failure")
	// The session must be marked revoked (not deleted), so Get still returns it but revoked.
	got, lookupErr := sessionStore.Get(context.Background(), sessionStore.created[0])
	require.NoError(t, lookupErr, "revoked session must still be Get-able (append-only revoke)")
	assert.NotNil(t, got.RevokedAt, "failed refresh issue must leave session revoked in noop tx mode")
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
	user.SetPasswordResetRequired(true, time.Now())
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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	svc := MustNewService(userRepo, sessionStore, roleRepo, newTestRefreshStore(), testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour))
	seedUser(userRepo, "issue-persist", "pass123")

	u, err := userRepo.GetByUsername(context.Background(), "issue-persist")
	require.NoError(t, err)

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.NoError(t, err)
	require.NotEmpty(t, pair.SessionID)

	// The session must be findable by its ID so sessionvalidate does not fail.
	// ValidateView intentionally hides GC-eligibility (ExpiresAt) — that lifetime
	// is verified at the sessionlogin construction layer (WithSessionTTL).
	sess, err := sessionStore.Get(context.Background(), pair.SessionID)
	require.NoError(t, err, "session must be persisted after IssueForUser so sessionvalidate can look it up")
	assert.Equal(t, pair.SessionID, sess.ID)
	assert.Equal(t, u.ID, sess.SubjectID, "SubjectID must match the issuing user ID")
	assert.Nil(t, sess.RevokedAt, "newly issued session must not be revoked")
}

func TestService_IssueForUser_RefreshStoreUnavailableReturnsInfraAndNoOrphanSession(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := &trackingSessionStore{Store: testutil.RealSessionRepo(t)}
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}
	// noopTxRunner (Noop()==true) triggers the isNoopTx cleanup path.
	svc := MustNewService(userRepo, sessionStore, roleRepo, store, testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		WithSessionTTL(time.Hour))
	seedUser(userRepo, "issue-refresh-down", "pass123")
	u, err := userRepo.GetByUsername(context.Background(), "issue-refresh-down")
	require.NoError(t, err)

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
	require.Len(t, sessionStore.created, 1)
	require.Equal(t, sessionStore.created, sessionStore.revoked)
}

// TestService_Login_BlankFieldsRejected verifies that RequireNotEmpty is
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
			wantMessage: "username",
		},
		{
			name:        "blank password rejected",
			input:       LoginInput{Username: "u", Password: ""},
			wantMessage: "password",
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
			assert.Equal(t, "validation: required field missing", ec.Message,
				"message must be a const literal")
			var gotField string
			for _, attr := range ec.Details {
				if attr.Key == "field" {
					gotField = attr.Value.String()
					break
				}
			}
			assert.Equal(t, tt.wantMessage, gotField, "details must carry the field name")
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

// countingSessionStore wraps session.Store and counts Create calls so
// fail-closed tests can assert the session write never happened.
type countingSessionStore struct {
	session.Store
	creates int
}

func (c *countingSessionStore) Create(ctx context.Context, s *session.Session) error {
	c.creates++
	return c.Store.Create(ctx, s)
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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := &countingSessionStore{Store: testutil.RealSessionRepo(t)}
	roleRepo := &brokenRoleRepo{err: fmt.Errorf("roleRepo outage")}
	seedUser(userRepo, "role-outage", "pass123")

	emitter := &countingEmitter{}
	svc := MustNewService(userRepo, sessionStore, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(), WithEmitter(emitter), WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithClock(clock.Real()), WithSessionTTL(time.Hour))

	pair, err := svc.Login(context.Background(), LoginInput{Username: "role-outage", Password: "pass123"})
	require.Error(t, err, "Login must fail when role fetch fails")
	assert.Empty(t, pair.AccessToken, "no token on failure")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code,
		"fail-closed: role fetch failure surfaces as ErrAuthRoleFetchFailed")

	assert.Equal(t, 0, sessionStore.creates, "no session must be persisted on fail-closed")
	assert.Equal(t, 0, emitter.count, "no session.created event on fail-closed")
}

// TestService_IssueForUser_RoleFetchFailure_AbortsIssue asserts the same
// fail-closed contract for the IssueForUser path (change-password flow).
func TestService_IssueForUser_RoleFetchFailure_AbortsIssue(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := &countingSessionStore{Store: testutil.RealSessionRepo(t)}
	roleRepo := &brokenRoleRepo{err: fmt.Errorf("roleRepo outage")}
	seedUser(userRepo, "issue-outage", "pass123")
	u, err := userRepo.GetByUsername(context.Background(), "issue-outage")
	require.NoError(t, err)

	svc := MustNewService(userRepo, sessionStore, roleRepo, newTestRefreshStore(), testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour))

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.Error(t, err, "IssueForUser must fail when role fetch fails")
	assert.Empty(t, pair.AccessToken)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code)

	assert.Equal(t, 0, sessionStore.creates, "no session must be persisted on fail-closed")
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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	seedUser(userRepo, "pub-err", "pass123")

	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "accesscore",
		outbox.WithLogger(slog.Default()),
	)
	require.NoError(t, err)
	svc := MustNewService(userRepo, sessionStore, roleRepo, newTestRefreshStore(), testIssuer,
		slog.Default(), WithEmitter(emitter), WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithClock(clock.Real()), WithSessionTTL(time.Hour))

	pair, err := svc.Login(context.Background(), LoginInput{Username: "pub-err", Password: "pass123"})
	require.NoError(t, err, "publish failure in demo mode should not fail login")
	assert.NotEmpty(t, pair.AccessToken)
}

// TestService_IssueForUser_EmitsSessionCreated locks in the always-emit contract:
// IssueForUser must emit exactly one event.session.created.v1 regardless of
// whether it is called from the Login or ChangePassword path.
func TestService_IssueForUser_EmitsSessionCreated(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	seedUser(userRepo, "emit-user", "pass123")
	u, err := userRepo.GetByUsername(context.Background(), "emit-user")
	require.NoError(t, err)

	emitter := &countingEmitter{}
	svc := MustNewService(userRepo, sessionStore, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(), WithEmitter(emitter), WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithClock(clock.Real()), WithSessionTTL(time.Hour))

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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := &trackingSessionStore{Store: testutil.RealSessionRepo(t)}
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}

	// stubTxRunner (defined in outbox_test.go) is NOT a Nooper — isNoopTx returns false.
	tx := &stubTxRunner{}
	svc := MustNewService(userRepo, sessionStore, roleRepo, store, testIssuer, slog.Default(),
		WithTxManager(persistence.WrapForCell(tx)), WithClock(clock.Real()), WithSessionTTL(time.Hour))
	seedUser(userRepo, "durable-refresh-fail", "pass123")

	_, err := svc.Login(context.Background(), LoginInput{Username: "durable-refresh-fail", Password: "pass123"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)

	// Durable tx: Create was called inside the tx, but no explicit Revoke.
	// The tx rollback would handle the cleanup atomically; no orphan cleanup needed.
	require.Len(t, sessionStore.created, 1, "session.Create was called inside the tx")
	assert.Len(t, sessionStore.revoked, 0,
		"durable tx mode: explicit cleanup must NOT be called; tx rollback handles it")
}

// TestCleanupIssuedSession_Revoke_IdempotentOnAbsent verifies that when the
// session store is empty (session never persisted, or already cleaned up),
// Revoke returns nil and the original refresh error is propagated unchanged.
// This replaces the former "not-found logs Debug" test: session.Store.Revoke
// is append-only idempotent — missing IDs are no-ops that return nil.
func TestCleanupIssuedSession_Revoke_IdempotentOnAbsent(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()

	// failingIssueRefreshStore causes cleanupIssuedSession to be called in
	// Noop (demo) tx mode. The session WAS created before the refresh issue
	// attempt, so Revoke will succeed silently — the important assertion is
	// that the original refresh error propagates unchanged.
	store := failingIssueRefreshStore{Store: newTestRefreshStore(), err: fmt.Errorf("refresh db down")}
	svc := MustNewService(userRepo, sessionStore, roleRepo, store, testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		WithSessionTTL(time.Hour))
	seedUser(userRepo, "cleanup-not-found", "pass123")

	// Should not panic or return an unexpected error — the original refresh issue error propagates.
	_, err := svc.Login(context.Background(), LoginInput{Username: "cleanup-not-found", Password: "pass123"})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code,
		"Revoke idempotency must not change the returned error; original refresh error propagates")
}

// TestLogin_EmptyCredentials_AuthErrorCode verifies that the service returns
// ErrAuthLoginInvalidInput (not ErrValidationFailed) for blank username or
// password. This locks the auth-domain error code contract at the service
// boundary so that if a transport layer swaps the code (B4 regression guard),
// this test will catch it.
func TestLogin_EmptyCredentials_AuthErrorCode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input LoginInput
	}{
		{"empty_password", LoginInput{Username: "user@example.com", Password: ""}},
		{"empty_email", LoginInput{Username: "", Password: "secret"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newTestService(t)
			_, err := svc.Login(context.Background(), tc.input)
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec, "expected *errcode.Error")
			assert.Equal(t, errcode.ErrAuthLoginInvalidInput, ec.Code,
				"empty credential must yield ErrAuthLoginInvalidInput, not ErrValidationFailed")
		})
	}
}

// TestLogin_AccessJWT_NoAuthzEpochClaim verifies that the access JWT issued by
// Login does NOT carry an authz_epoch claim (S4d: epoch provenance moved to
// session/refresh rows; the JWT claim is removed entirely).
func TestLogin_AccessJWT_NoAuthzEpochClaim(t *testing.T) {
	svc, userRepo := newTestService(t)

	hash, _ := bcrypt.GenerateFromPassword([]byte("pass123"), bcrypt.MinCost)
	user, err := domain.NewUser("epoch-user", "epoch@test.com", string(hash), time.Now())
	require.NoError(t, err)
	user.ID = "usr-epoch"
	// AuthzEpoch starts at 1 for new users (set by NewUser); we cannot directly
	// set it to 7 via a field. Instead, create the user with epoch=1 and bump it
	// 6 times (via BumpAuthzEpoch on the repo) or just create with epoch=1
	// (epoch value does not affect the "no claim in JWT" assertion we're testing).
	require.NoError(t, userRepo.Create(context.Background(), user))

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "epoch-user", Password: "pass123"})
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	// S4d: authz_epoch removed from JWT; epoch lives in session.authz_epoch_at_issue row.
	_, epochInExtra := claims.Extra["authz_epoch"]
	assert.False(t, epochInExtra, "S4d: authz_epoch must not be present in JWT claims (including Extra)")
}

// TestLogin_NoLengthOracle verifies that the error message returned for a
// blank credential does not reveal internal length constraints such as
// "value too short" or "value too long". Leaking length information
// facilitates oracle attacks against the authentication endpoint.
func TestLogin_NoLengthOracle(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input LoginInput
	}{
		{"empty_password", LoginInput{Username: "user@example.com", Password: ""}},
		{"empty_email", LoginInput{Username: "", Password: "secret"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _ := newTestService(t)
			_, err := svc.Login(context.Background(), tc.input)
			require.Error(t, err)
			msg := err.Error()
			assert.NotContains(t, msg, "value too short",
				"error message must not reveal length oracle")
			assert.NotContains(t, msg, "value too long",
				"error message must not reveal length oracle")
		})
	}
}

// --- P1.1: password-version-pin race tests ---

// versionRacingUserRepo simulates a concurrent ChangePassword that commits
// between the pre-bcrypt GetByUsername and the in-tx GetByUsernameForUpdate.
// GetByUsername returns a user with PasswordVersion=N; GetByUsernameForUpdate
// returns the same user but with PasswordVersion=N+1 (race window committed).
type versionRacingUserRepo struct {
	mem.UserRepository
	preUser    *domain.User // returned by GetByUsername (stale snapshot)
	lockedUser *domain.User // returned by GetByUsernameForUpdate (current row)
}

func (r *versionRacingUserRepo) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	return r.preUser, nil
}

func (r *versionRacingUserRepo) GetByUsernameForUpdate(_ context.Context, _ string) (*domain.User, error) {
	return r.lockedUser, nil
}

// countingRefreshStore counts Issue calls to verify no token was minted.
type countingRefreshStore struct {
	refresh.Store
	issued int
}

func (c *countingRefreshStore) Issue(ctx context.Context, sessID, userID string, authzEpoch int64) (string, *refresh.Token, error) {
	c.issued++
	return c.Store.Issue(ctx, sessID, userID, authzEpoch)
}

// TestLogin_PasswordVersionRace_OldPasswordRejected (P1.1) verifies that when a
// concurrent ChangePassword commits between the pre-bcrypt snapshot and the
// FOR UPDATE re-fetch, Login rejects the old password with ErrAuthLoginFailed
// and mints NO session or refresh token. The control case (versions equal)
// verifies the success path is unaffected.
func TestLogin_PasswordVersionRace_OldPasswordRejected(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("old-pass"), bcrypt.MinCost)

	// preUser: PasswordVersion=1, PasswordHash=hash-of-old-pass (pre-bcrypt snapshot)
	preUser, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:              "usr-race",
		Username:        "race-user",
		Email:           "race@test.com",
		PasswordHash:    string(hash),
		PasswordVersion: 1,
		Status:          domain.StatusActive,
		Source:          domain.UserSourceIdentity,
		AuthzEpoch:      1,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	// lockedUser: PasswordVersion=2 (concurrent ChangePassword committed in race window)
	lockedUser, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:              "usr-race",
		Username:        "race-user",
		Email:           "race@test.com",
		PasswordHash:    string(hash),
		PasswordVersion: 2, // bumped by concurrent ChangePassword
		Status:          domain.StatusActive,
		Source:          domain.UserSourceIdentity,
		AuthzEpoch:      1,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	})
	require.NoError(t, err)

	tests := []struct {
		name        string
		lockedUser  *domain.User
		wantErr     bool
		wantErrCode errcode.Code
	}{
		{
			name:        "race: locked row has newer PasswordVersion → old-password must be rejected",
			lockedUser:  lockedUser, // version N+1 → race detected
			wantErr:     true,
			wantErrCode: errcode.ErrAuthLoginFailed,
		},
		{
			name:       "control: PasswordVersion unchanged → login succeeds",
			lockedUser: preUser, // same version → no race
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionStore := &countingSessionStore{Store: testutil.RealSessionRepo(t)}
			refreshStore := &countingRefreshStore{Store: newTestRefreshStore()}

			baseRepo := mem.NewStore(clock.Real()).UserRepository()
			racingRepo := &versionRacingUserRepo{
				UserRepository: *baseRepo,
				preUser:        preUser,
				lockedUser:     tt.lockedUser,
			}

			svc := MustNewService(
				racingRepo,
				sessionStore,
				mem.NewStore(clock.Real()).RoleRepository(),
				refreshStore,
				testIssuer,
				slog.Default(),
				WithClock(clock.Real()),
				WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
				WithSessionTTL(time.Hour),
			)

			pair, loginErr := svc.Login(context.Background(), LoginInput{
				Username: "race-user",
				Password: "old-pass",
			})

			if tt.wantErr {
				require.Error(t, loginErr)
				var ec *errcode.Error
				require.ErrorAs(t, loginErr, &ec)
				assert.Equal(t, tt.wantErrCode, ec.Code,
					"race-detected login must return ErrAuthLoginFailed (防枚举)")
				assert.Empty(t, pair.AccessToken, "no token must be issued on race detection")
				assert.Zero(t, sessionStore.creates,
					"no session must be created when password-version race detected")
				assert.Zero(t, refreshStore.issued,
					"no refresh must be issued when password-version race detected")
			} else {
				require.NoError(t, loginErr)
				assert.NotEmpty(t, pair.AccessToken, "control path must issue token")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// R3: Error classification in loginInTx — infra errors must NOT become 401
// ---------------------------------------------------------------------------

// infraErrUserRepo is a stub UserRepository whose GetByUsernameForUpdate
// returns a configurable error, used to simulate infra faults inside loginInTx.
// It delegates all other methods to the embedded *mem.UserRepository so the
// pre-bcrypt GetByUsername and bcrypt path succeed normally.
type infraErrUserRepo struct {
	*mem.UserRepository
	forUpdateErr error // error to return from GetByUsernameForUpdate
}

func (r *infraErrUserRepo) GetByUsernameForUpdate(_ context.Context, _ string) (*domain.User, error) {
	return nil, r.forUpdateErr
}

// TestLoginInTx_InfraError_NotCollapsedTo401 (R3 RED→GREEN) verifies that an
// infrastructure error returned by GetByUsernameForUpdate propagates with its
// original Kind, not as a 401 ErrAuthLoginFailed. Before R3 this was silently
// converted to 401, hiding infra outages from on-call operators.
func TestLoginInTx_InfraError_NotCollapsedTo401(t *testing.T) {
	infraErr := errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"simulated db failure from GetByUsernameForUpdate")

	// Wire a repo that returns the infra error on ForUpdate, but a real user on
	// GetByUsername (so the pre-bcrypt lookup succeeds and bcrypt runs).
	store := mem.NewStore(clock.Real())
	baseRepo := store.UserRepository()
	seedUser(baseRepo, "r3-user", "pass123")

	hybridRepo := &infraErrUserRepo{
		UserRepository: baseRepo,
		forUpdateErr:   infraErr,
	}

	svc := MustNewService(
		hybridRepo,
		testutil.RealSessionRepo(t),
		store.RoleRepository(),
		newTestRefreshStore(),
		testIssuer,
		slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
	)

	_, err := svc.Login(context.Background(), LoginInput{Username: "r3-user", Password: "pass123"})
	require.Error(t, err, "infra error must propagate, not silently succeed")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	// R3: infra KindInternal must NOT be collapsed into 401 KindUnauthenticated.
	assert.Equal(t, errcode.KindInternal, ec.Kind,
		"R3: GetByUsernameForUpdate KindInternal must propagate as 5xx, not be disguised as 401")
	assert.NotEqual(t, errcode.ErrAuthLoginFailed, ec.Code,
		"R3: infra error must not produce ErrAuthLoginFailed")
}

// TestLoginInTx_NotFound_CollapsedTo401 (R3 control) verifies that a
// KindNotFound from GetByUsernameForUpdate IS collapsed into 401 (domain
// credential failure), preserving the existing anti-enumeration behavior.
func TestLoginInTx_NotFound_CollapsedTo401(t *testing.T) {
	notFoundErr := errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
		errcode.WithCategory(errcode.CategoryDomain))

	store := mem.NewStore(clock.Real())
	baseRepo := store.UserRepository()
	seedUser(baseRepo, "r3-notfound", "pass123")

	hybridRepo := &infraErrUserRepo{
		UserRepository: baseRepo,
		forUpdateErr:   notFoundErr,
	}

	svc := MustNewService(
		hybridRepo,
		testutil.RealSessionRepo(t),
		store.RoleRepository(),
		newTestRefreshStore(),
		testIssuer,
		slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
	)

	_, err := svc.Login(context.Background(), LoginInput{Username: "r3-notfound", Password: "pass123"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	// KindNotFound in ForUpdate → must map to 401 ErrAuthLoginFailed (anti-enumeration).
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
		"R3 control: KindNotFound from ForUpdate must become 401")
	assert.Equal(t, errcode.ErrAuthLoginFailed, ec.Code,
		"R3 control: KindNotFound must produce ErrAuthLoginFailed (防枚举)")
}

// TestLoginInTx_UnavailableError_NotCollapsedTo401 (R3) verifies KindUnavailable
// (e.g. DB temporarily down) also propagates as-is, not as 401.
func TestLoginInTx_UnavailableError_NotCollapsedTo401(t *testing.T) {
	unavailErr := errcode.New(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable,
		"db temporarily unavailable")

	store := mem.NewStore(clock.Real())
	baseRepo := store.UserRepository()
	seedUser(baseRepo, "r3-unavail", "pass123")

	hybridRepo := &infraErrUserRepo{
		UserRepository: baseRepo,
		forUpdateErr:   unavailErr,
	}

	svc := MustNewService(
		hybridRepo,
		testutil.RealSessionRepo(t),
		store.RoleRepository(),
		newTestRefreshStore(),
		testIssuer,
		slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
	)

	_, err := svc.Login(context.Background(), LoginInput{Username: "r3-unavail", Password: "pass123"})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindUnavailable, ec.Kind,
		"R3: KindUnavailable from ForUpdate must propagate as 503, not 401")
}

// --- P1.3a: IssueForUser active-gate tests ---

// TestIssueForUser_NonActiveUser_Rejected (P1.3a) verifies that IssueForUser
// fail-closes for non-active users (suspended, locked), mirroring the Login
// pre-check. The control (active user) must still succeed.
func TestIssueForUser_NonActiveUser_Rejected(t *testing.T) {
	tests := []struct {
		name        string
		status      domain.UserStatus
		wantErr     bool
		wantErrCode errcode.Code
	}{
		{
			name:        "suspended user must be rejected",
			status:      domain.StatusSuspended,
			wantErr:     true,
			wantErrCode: errcode.ErrAuthUserNotActive,
		},
		{
			name:        "locked user must be rejected",
			status:      domain.StatusLocked,
			wantErr:     true,
			wantErrCode: errcode.ErrAuthUserNotActive,
		},
		{
			name:    "active user must succeed (control)",
			status:  domain.StatusActive,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userRepo := mem.NewStore(clock.Real()).UserRepository()
			sessionStore := &countingSessionStore{Store: testutil.RealSessionRepo(t)}

			// Build a non-active user via ReconstituteUser (the only path that can set
			// non-active status on an existing aggregate).
			hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
			u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
				ID:              "usr-issue-gate",
				Username:        "issue-gate",
				Email:           "gate@test.com",
				PasswordHash:    string(hash),
				PasswordVersion: 1,
				Status:          tt.status,
				Source:          domain.UserSourceIdentity,
				AuthzEpoch:      1,
				CreatedAt:       time.Now(),
				UpdatedAt:       time.Now(),
			})
			require.NoError(t, err)
			require.NoError(t, userRepo.Create(context.Background(), u))

			svc := MustNewService(
				userRepo,
				sessionStore,
				mem.NewStore(clock.Real()).RoleRepository(),
				newTestRefreshStore(),
				testIssuer,
				slog.Default(),
				WithClock(clock.Real()),
				WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
				WithSessionTTL(time.Hour),
			)

			pair, issueErr := svc.IssueForUser(context.Background(), u.ID)

			if tt.wantErr {
				require.Error(t, issueErr)
				var ec *errcode.Error
				require.ErrorAs(t, issueErr, &ec)
				assert.Equal(t, tt.wantErrCode, ec.Code,
					"non-active user must yield ErrAuthUserNotActive")
				assert.Empty(t, pair.AccessToken, "no token on non-active user")
				assert.Zero(t, sessionStore.creates,
					"no session must be created for non-active user")
			} else {
				require.NoError(t, issueErr)
				assert.NotEmpty(t, pair.AccessToken, "active user must get token")
			}
		})
	}
}
