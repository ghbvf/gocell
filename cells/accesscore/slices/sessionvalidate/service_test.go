package sessionvalidate

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

var (
	testKeySet, testPrivKey, _ = auth.MustNewTestKeySet(clock.Real())
	testVerifier               *auth.JWTVerifier
)

func init() {
	var err error
	testVerifier, err = auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

// dNeg2h is the offset for seeding an expired session whose CreatedAt is 2h ago.
const dNeg2h = -2 * time.Hour

// mustBuildUser creates a minimal domain.User for use in test stubs.
// It uses ReconstituteUser so that the private authzEpoch field is set correctly.
func mustBuildUser(t testing.TB, id string, epoch int64) *domain.User {
	t.Helper()
	now := time.Now()
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{ //nolint:gosec // G101: test fixture
		ID:           id,
		Username:     id,
		Email:        id + "@test.local",
		PasswordHash: "$2a$12$hash",
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   epoch,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	require.NoError(t, err)
	return u
}

// testProtocol returns a Protocol suitable for in-memory session tests.
// FingerprintJTIRef requires a non-empty JTI on every seeded Session.
func testProtocol(t testing.TB) *session.Protocol {
	t.Helper()
	p, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	require.NoError(t, err)
	return p
}

// newTestStore constructs a MemStore for tests.
func newTestStore(t testing.TB) *session.MemStore {
	t.Helper()
	store, err := session.NewMemStore(testProtocol(t), clock.Real())
	require.NoError(t, err)
	return store
}

func TestService_VerifyIntent(t *testing.T) {
	store := newTestStore(t)

	// Seed an active session for revocation tests.
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                "sess-active",
		SubjectID:         "usr-1",
		JTI:               "jti-active",
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}))

	// Seed a revoked session.
	revokedAt := time.Now()
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                "sess-revoked",
		SubjectID:         "usr-2",
		JTI:               "jti-revoked",
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
		RevokedAt:         &revokedAt,
	}))

	tests := []struct {
		name    string
		token   func() string
		wantSub string
		wantErr bool
	}{
		{
			name: "valid token without sid",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", []string{"admin"}, time.Hour)
				return tok
			},
			wantSub: "usr-1",
			wantErr: true,
		},
		{
			name: "valid token with active session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", []string{"admin"}, time.Hour, "sess-active")
				return tok
			},
			wantSub: "usr-1",
			wantErr: false,
		},
		{
			name: "token with revoked session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-2", nil, time.Hour, "sess-revoked")
				return tok
			},
			wantErr: true,
		},
		{
			name: "token with non-existent session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-nonexistent")
				return tok
			},
			wantErr: true,
		},
		{
			name:    "empty token",
			token:   func() string { return "" },
			wantErr: true,
		},
		{
			name:    "invalid token",
			token:   func() string { return "bad.token.here" },
			wantErr: true,
		},
		{
			name: "expired token",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-1", nil, -time.Hour)
				return tok
			},
			wantErr: true,
		},
		{
			name: "wrong signing key",
			token: func() string {
				wrongPriv, _ := auth.MustGenerateTestKeyPair()
				tok, _ := IssueTestToken(wrongPriv, "usr-1", nil, time.Hour)
				return tok
			},
			wantErr: true,
		},
	}

	// S4d: epoch comparison is user.AuthzEpoch vs view.AuthzEpochAtIssue (row-based).
	// sess-active was seeded with AuthzEpochAtIssue=1; user must have AuthzEpoch=1 to pass.
	userRepo := &stubUserRepo{user: mustBuildUser(t, "usr-1", 1)}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSvcWithUserRepo(t, store, userRepo)

			claims, err := svc.VerifyIntent(context.Background(), tt.token(), auth.TokenIntentAccess)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantSub, claims.Subject)
				if tt.name == "valid token with active session" {
					assert.Contains(t, claims.Roles, "admin")
				}
				assert.Equal(t, "gocell-accesscore", claims.Issuer)
			}
		})
	}
}

// TestService_VerifyIntent_PastSessionExpiresAt_StillValidates is the F1
// regression guard: session row's ExpiresAt is GC eligibility (migration
// 018 idx_sessions_expires), not a validate gate. JWT exp claim already
// guards access-token lifetime; rejecting on sess.ExpiresAt was a dead
// double-gate that fires after refresh keeps sid stable but doesn't
// extend the session row's ExpiresAt — yielding a fresh JWT that any
// refresh succeeded on but validate rejects.
//
// ref: ory/fosite handler/oauth2/strategy_jwt.go ValidateAccessToken
// (JWT exp only); hashicorp/vault token_store.go lookupInternal
// (lease ExpireTime not reachable from token lookup path).
func TestService_VerifyIntent_PastSessionExpiresAt_StillValidates(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                "sess-row-past",
		SubjectID:         "usr-row-past",
		JTI:               "jti-row-past",
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(-time.Hour), // session row "past" — GC eligible
		CreatedAt:         time.Now().Add(dNeg2h),
	}))

	// Fresh JWT bound to the same sid — mirrors a refresh-issued token.
	tok, err := IssueTestToken(testPrivKey, "usr-row-past", nil, time.Hour, "sess-row-past")
	require.NoError(t, err)

	// S4d: session has AuthzEpochAtIssue=1; user must match.
	userRepo := &stubUserRepo{user: mustBuildUser(t, "usr-row-past", 1)}
	svc := newSvcWithUserRepo(t, store, userRepo)
	claims, err := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err,
		"F1: sessionvalidate must NOT reject on session-row ExpiresAt; JWT exp + RevokedAt are the validate gates")
	assert.Equal(t, "usr-row-past", claims.Subject)
}

func TestService_VerifyIntent_NilSessionStore(t *testing.T) {
	// When sessionStore is nil (backward compatibility), sid claim is ignored.
	// userRepo is required by constructor but is never called when sessionStore is nil.
	svc := newSvcWithUserRepo(t, nil, &stubUserRepo{})

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-any")
	require.NoError(t, err)

	claims, err := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr-1", claims.Subject)
}

// errorSessionStore simulates infrastructure failures (DB timeout, connection reset).
type errorSessionStore struct{}

func (errorSessionStore) Create(_ context.Context, _ *session.Session) error { return nil }
func (errorSessionStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	return nil, fmt.Errorf("db connection timeout")
}
func (errorSessionStore) Revoke(_ context.Context, _ string) error { return nil }
func (errorSessionStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	return nil
}

func TestService_VerifyIntent_DBError_FailsClosed(t *testing.T) {
	// Infrastructure errors from the session store are fail-closed: they surface
	// as ERR_AUTH_SERVICE_UNAVAILABLE (503) so operators can distinguish transient
	// infra outages from invalid-credential rejections (S4b Batch 3H).
	// userRepo is never reached because errorSessionStore returns an error first.
	svc := newSvcWithUserRepo(t, errorSessionStore{}, &stubUserRepo{})

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-db-fail")
	require.NoError(t, err)

	_, err = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "DB errors must cause verification failure (fail-closed)")
	assert.Contains(t, err.Error(), "ERR_AUTH_SERVICE_UNAVAILABLE",
		"session store infra error must surface as 503 ERR_AUTH_SERVICE_UNAVAILABLE")
}

func TestService_VerifyIntent_NilSessionStore_NoSid(t *testing.T) {
	// When sessionStore is nil (demo mode), tokens without sid are accepted.
	// userRepo is required by constructor but is never called when sessionStore is nil.
	svc := newSvcWithUserRepo(t, nil, &stubUserRepo{})

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour)
	require.NoError(t, err)

	claims, err := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr-1", claims.Subject)
}

// stubUserRepo is a minimal ports.UserRepository for epoch tests.
// Only GetByID is exercised by sessionvalidate; other methods panic so that
// accidentally-invoked paths fail loudly in tests.
type stubUserRepo struct {
	// user is returned on GetByID when getErr is nil.
	user *domain.User
	// getErr, if non-nil, is returned from GetByID.
	getErr error
}

var _ ports.UserRepository = (*stubUserRepo)(nil)

func (r *stubUserRepo) GetByID(_ context.Context, _ string) (*domain.User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.user != nil {
		return r.user, nil
	}
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
		errcode.WithCategory(errcode.CategoryDomain))
}

func (r *stubUserRepo) Create(_ context.Context, _ *domain.User) error { panic("not implemented") }
func (r *stubUserRepo) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	panic("not implemented")
}
func (r *stubUserRepo) Update(_ context.Context, _ *domain.User) error { panic("not implemented") }
func (r *stubUserRepo) Delete(_ context.Context, _ string) error       { panic("not implemented") }
func (r *stubUserRepo) UpdatePassword(_ context.Context, _ string, _ string, _ bool, _ int64) (int64, error) {
	panic("not implemented")
}

func (r *stubUserRepo) BumpAuthzEpoch(_ context.Context, _ string) (int64, error) {
	panic("not implemented")
}

func (r *stubUserRepo) GetByIDForUpdate(_ context.Context, _ string) (*domain.User, error) {
	panic("not implemented")
}

func (r *stubUserRepo) GetByUsernameForUpdate(_ context.Context, _ string) (*domain.User, error) {
	panic("not implemented")
}

// newSvcWithUserRepo is a helper that builds a Service wired with both a
// session store and a user repo. Existing tests that don't exercise epoch logic
// use newTestSvc (nil userRepo) via the sessionStore-only path.
func newSvcWithUserRepo(t testing.TB, store session.Store, userRepo ports.UserRepository) *Service {
	t.Helper()
	svc, err := NewService(testVerifier, store, userRepo, slog.Default())
	require.NoError(t, err)
	return svc
}

// TestNewService_NilGuards verifies that NewService fail-fasts on nil required deps.
// Finding #3: verifier nil-check added alongside existing userRepo check.
func TestNewService_NilGuards(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	userRepo := &stubUserRepo{user: &domain.User{ID: "usr-1"}}

	cases := []struct {
		name     string
		verifier auth.IntentTokenVerifier
		userRepo ports.UserRepository
	}{
		{
			name:     "nil verifier returns error",
			verifier: nil,
			userRepo: userRepo,
		},
		{
			name:     "typed-nil verifier returns error",
			verifier: (*auth.JWTVerifier)(nil),
			userRepo: userRepo,
		},
		{
			name:     "nil userRepo returns error",
			verifier: testVerifier,
			userRepo: nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, err := NewService(tc.verifier, store, tc.userRepo, slog.Default())
			require.Error(t, err, "NewService must fail on nil dep: %s", tc.name)
			assert.Nil(t, svc)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.KindInvalid, ec.Kind)
			assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
		})
	}
}

// seedActiveSession seeds a session in store and returns its ID.
func seedActiveSession(t testing.TB, store *session.MemStore, sid, subject string) {
	t.Helper()
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                sid,
		SubjectID:         subject,
		JTI:               sid + "-jti",
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}))
}

// capturingStore wraps a real or stub session store and allows injecting errors
// with specific errcode categories for logSessionLookupError tests.
type capturingStore struct {
	getErr error
}

func (r capturingStore) Create(_ context.Context, _ *session.Session) error { return nil }
func (r capturingStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	return nil, r.getErr
}
func (r capturingStore) Revoke(_ context.Context, _ string) error { return nil }
func (r capturingStore) RevokeForSubject(_ context.Context, _ string, _ session.CredentialEvent) error {
	return nil
}

// capturingUserRepo is a ports.UserRepository whose GetByID injects a
// configurable error for infra-error-path tests.
type capturingUserRepo struct {
	getErr error
	user   *domain.User
}

var _ ports.UserRepository = (*capturingUserRepo)(nil)

func (r *capturingUserRepo) GetByID(_ context.Context, _ string) (*domain.User, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.user != nil {
		return r.user, nil
	}
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
		errcode.WithCategory(errcode.CategoryDomain))
}
func (r *capturingUserRepo) Create(_ context.Context, _ *domain.User) error { return nil }
func (r *capturingUserRepo) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "not implemented",
		errcode.WithCategory(errcode.CategoryDomain))
}
func (r *capturingUserRepo) Update(_ context.Context, _ *domain.User) error { return nil }
func (r *capturingUserRepo) Delete(_ context.Context, _ string) error       { return nil }
func (r *capturingUserRepo) UpdatePassword(_ context.Context, _ string, _ string, _ bool, _ int64) (int64, error) {
	return 0, nil
}

func (r *capturingUserRepo) BumpAuthzEpoch(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

func (r *capturingUserRepo) GetByIDForUpdate(_ context.Context, _ string) (*domain.User, error) {
	panic("not implemented")
}

func (r *capturingUserRepo) GetByUsernameForUpdate(_ context.Context, _ string) (*domain.User, error) {
	panic("not implemented")
}

// --- S4d row-provenance epoch-compare tests ---
//
// S4d change: sessionvalidate now compares user.AuthzEpoch against
// view.AuthzEpochAtIssue (the epoch stored on the session row at login time),
// not the JWT claim. JWT carries no authz_epoch claim.
//
// Accept case: user.AuthzEpoch == view.AuthzEpochAtIssue (no credential event
// since this session was created).
// Reject case: user.AuthzEpoch != view.AuthzEpochAtIssue (credential event
// bumped user epoch after this session was created, invalidating the session).

// TestEnforce_StaleEpoch_Rejected verifies that when user.AuthzEpoch (5) differs
// from session.AuthzEpochAtIssue (1), the service rejects with 401. This guards
// the primary S4d credential-revocation path: a credential event bumps user.epoch,
// making all sessions issued at the prior epoch stale.
func TestEnforce_StaleEpoch_Rejected(t *testing.T) {
	store := newTestStore(t)
	// seedActiveSession seeds AuthzEpochAtIssue=1.
	seedActiveSession(t, store, "sess-epoch-stale", "usr-epoch")

	// user.epoch bumped to 5 (simulates credential event after session was created).
	user := mustBuildUser(t, "usr-epoch", 5)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestToken(testPrivKey, "usr-epoch", nil, time.Hour, "sess-epoch-stale")
	require.NoError(t, err)

	_, verifyErr := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, verifyErr)
	assert.Contains(t, verifyErr.Error(), errMsgAuthFailed,
		"stale epoch must return uniform auth-failed message")
	assert.NotContains(t, verifyErr.Error(), "ERR_AUTH_SERVICE_UNAVAILABLE",
		"stale epoch must not return 503 code")
}

// TestEnforce_EqualEpoch_Accepted verifies that user.AuthzEpoch == session.AuthzEpochAtIssue
// accepts the token (no credential event since this session was created).
func TestEnforce_EqualEpoch_Accepted(t *testing.T) {
	store := newTestStore(t)
	// Seed session with AuthzEpochAtIssue=5 to match user.AuthzEpoch=5.
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                "sess-epoch-equal",
		SubjectID:         "usr-ep-equal",
		JTI:               "sess-epoch-equal-jti",
		AuthzEpochAtIssue: 5,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}))

	user := mustBuildUser(t, "usr-ep-equal", 5)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestToken(testPrivKey, "usr-ep-equal", nil, time.Hour, "sess-epoch-equal")
	require.NoError(t, err)

	claims, err := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr-ep-equal", claims.Subject)
}

// TestEnforce_InitialEpochCompat verifies that user.AuthzEpoch == session.AuthzEpochAtIssue == 1
// passes (S4d initial state: domain.NewUser initializes AuthzEpoch=1; first session is issued
// at epoch 1 via sessionlogin).
func TestEnforce_InitialEpochCompat(t *testing.T) {
	store := newTestStore(t)
	// seedActiveSession seeds AuthzEpochAtIssue=1 — matches initial user epoch.
	seedActiveSession(t, store, "sess-epoch-initial", "usr-ep-initial")

	user := mustBuildUser(t, "usr-ep-initial", 1)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestToken(testPrivKey, "usr-ep-initial", nil, time.Hour, "sess-epoch-initial")
	require.NoError(t, err)

	_, err = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err, "initial epoch=1 session must pass (S4d initial state)")
}

// TestEnforce_RowEpochAheadOfUser_Rejected verifies that session.AuthzEpochAtIssue > user.AuthzEpoch
// is rejected with 401 (fail-closed: any epoch mismatch is invalid regardless of direction).
// Row epoch ahead of user epoch cannot occur normally (sessions are created with user's current
// epoch), but defense-in-depth rejects any mismatch. Finding #2: != is used not >, so
// future-epoch sessions (if somehow created) are also rejected.
func TestEnforce_RowEpochAheadOfUser_Rejected(t *testing.T) {
	store := newTestStore(t)
	// Seed session with epoch=10, but user is at epoch=5.
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                "sess-epoch-high",
		SubjectID:         "usr-ep-high",
		JTI:               "sess-epoch-high-jti",
		AuthzEpochAtIssue: 10,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}))

	user := mustBuildUser(t, "usr-ep-high", 5)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestToken(testPrivKey, "usr-ep-high", nil, time.Hour, "sess-epoch-high")
	require.NoError(t, err)

	_, err = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "row epoch ahead of user epoch must be rejected (fail-closed mismatch)")
	assert.Contains(t, err.Error(), errMsgAuthFailed,
		"epoch mismatch must return uniform auth-failed message")
}

// TestEnforce_SessionInfraError_Returns503 verifies that an infra error from
// sessionStore.Get surfaces as KindUnavailable + ErrAuthServiceUnavailable.
func TestEnforce_SessionInfraError_Returns503(t *testing.T) {
	infraErr := errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "db down")
	store := capturingStore{getErr: infraErr}
	// Session store will return infra error before user is fetched; epoch doesn't matter.
	user := mustBuildUser(t, "usr-infra", 1)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestTokenWithEpoch(testPrivKey, "usr-infra", 0, time.Hour, "sess-infra")
	require.NoError(t, err)

	_, verifyErr := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, verifyErr)

	var ec *errcode.Error
	require.ErrorAs(t, verifyErr, &ec, "error must be an errcode.Error")
	assert.Equal(t, errcode.KindUnavailable, ec.Kind, "session infra error must surface as KindUnavailable")
	assert.Equal(t, errcode.ErrAuthServiceUnavailable, ec.Code, "session infra error must have ErrAuthServiceUnavailable code")
}

// TestEnforce_UserRepoInfraError_Returns503 verifies that an infra error from
// userRepo.GetByID surfaces as KindUnavailable + ErrAuthServiceUnavailable.
func TestEnforce_UserRepoInfraError_Returns503(t *testing.T) {
	store := newTestStore(t)
	seedActiveSession(t, store, "sess-userrepo-infra", "usr-repo-infra")

	infraErr := errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable, "user store down")
	svc := newSvcWithUserRepo(t, store, &capturingUserRepo{getErr: infraErr})

	tok, err := IssueTestTokenWithEpoch(testPrivKey, "usr-repo-infra", 0, time.Hour, "sess-userrepo-infra")
	require.NoError(t, err)

	_, verifyErr := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, verifyErr)

	var ec *errcode.Error
	require.ErrorAs(t, verifyErr, &ec, "error must be an errcode.Error")
	assert.Equal(t, errcode.KindUnavailable, ec.Kind, "user repo infra error must surface as KindUnavailable")
	assert.Equal(t, errcode.ErrAuthServiceUnavailable, ec.Code, "user repo infra error must have ErrAuthServiceUnavailable code")
}

// TestEnforce_DomainNotFound_Returns401 verifies that a domain not-found error
// from sessionStore.Get returns a 401 uniform body, not a 503.
func TestEnforce_DomainNotFound_Returns401(t *testing.T) {
	domainNotFound := errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found",
		errcode.WithCategory(errcode.CategoryDomain))
	store := capturingStore{getErr: domainNotFound}
	// Session store returns domain not-found before user is fetched; epoch doesn't matter.
	user := mustBuildUser(t, "usr-notfound", 1)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestTokenWithEpoch(testPrivKey, "usr-notfound", 0, time.Hour, "sess-notfound")
	require.NoError(t, err)

	_, verifyErr := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, verifyErr)

	var ec *errcode.Error
	require.ErrorAs(t, verifyErr, &ec, "error must be an errcode.Error")
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
		"domain not-found from session store must map to 401, not 503")
	assert.NotEqual(t, errcode.ErrAuthServiceUnavailable, ec.Code,
		"domain not-found must not return ErrAuthServiceUnavailable")
}

// TestEnforce_UniformAuthFailedBody verifies that stale epoch, revoked session,
// and user not-found all return the same errMsgAuthFailed message (anti-enumeration).
func TestEnforce_UniformAuthFailedBody(t *testing.T) {
	store := newTestStore(t)

	// Seed an active session (for stale-epoch and user-not-found cases).
	seedActiveSession(t, store, "sess-uniform", "usr-uniform")

	// Seed a revoked session.
	revokedAt := time.Now()
	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                "sess-revoked-uniform",
		SubjectID:         "usr-uniform",
		JTI:               "jti-revoked-uniform",
		AuthzEpochAtIssue: 1,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
		RevokedAt:         &revokedAt,
	}))

	tests := []struct {
		name    string
		tok     func() string
		userRep ports.UserRepository
	}{
		{
			name: "stale epoch",
			tok: func() string {
				tok, _ := IssueTestTokenWithEpoch(testPrivKey, "usr-uniform", 1, time.Hour, "sess-uniform")
				return tok
			},
			userRep: &stubUserRepo{user: mustBuildUser(t, "usr-uniform", 5)},
		},
		{
			name: "revoked session",
			tok: func() string {
				tok, _ := IssueTestTokenWithEpoch(testPrivKey, "usr-uniform", 5, time.Hour, "sess-revoked-uniform")
				return tok
			},
			userRep: &stubUserRepo{user: mustBuildUser(t, "usr-uniform", 5)},
		},
		{
			name: "user domain not found",
			tok: func() string {
				tok, _ := IssueTestTokenWithEpoch(testPrivKey, "usr-uniform", 0, time.Hour, "sess-uniform")
				return tok
			},
			// stubUserRepo with nil user returns domain not-found.
			userRep: &stubUserRepo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newSvcWithUserRepo(t, store, tt.userRep)
			_, err := svc.VerifyIntent(context.Background(), tt.tok(), auth.TokenIntentAccess)
			require.Error(t, err)
			assert.Contains(t, err.Error(), errMsgAuthFailed,
				"all auth failures must return the uniform body to prevent enumeration")
		})
	}
}

// mustBuildUserWithStatus creates a domain.User with the given status and epoch
// via ReconstituteUser. Used for P1.3b CanAuthenticate gate tests.
func mustBuildUserWithStatus(t testing.TB, id string, epoch int64, status domain.UserStatus) *domain.User {
	t.Helper()
	now := time.Now()
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{ //nolint:gosec // G101: test fixture
		ID:           id,
		Username:     id,
		Email:        id + "@test.local",
		PasswordHash: "$2a$12$hash",
		Status:       status,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   epoch,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	require.NoError(t, err)
	return u
}

// TestEnforce_NonActiveUser_Rejected_P1_3b is the P1.3b regression guard:
// a session that exists and is NOT revoked, with epoch MATCHING
// (user.AuthzEpoch == view.AuthzEpochAtIssue), but the user account became
// non-active (suspended / locked) after the token was issued.
// enforceSessionState must reject with the uniform 401 (KindUnauthenticated /
// ErrAuthInvalidToken) — the CanAuthenticate gate runs AFTER epoch match,
// closing the window where a valid epoch does not imply login eligibility.
func TestEnforce_NonActiveUser_Rejected_P1_3b(t *testing.T) {
	const (
		epochAtIssue = int64(3) // deliberately non-1 to prove epoch match still checked
	)

	tests := []struct {
		name   string
		status domain.UserStatus
	}{
		{name: "suspended user rejected", status: domain.StatusSuspended},
		{name: "locked user rejected", status: domain.StatusLocked},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(t)
			sid := "sess-noactive-" + string(tt.status)
			sub := "usr-noactive-" + string(tt.status)

			require.NoError(t, store.Create(context.Background(), &session.Session{
				ID:                sid,
				SubjectID:         sub,
				JTI:               sid + "-jti",
				AuthzEpochAtIssue: epochAtIssue, // epoch stored on row at issue time
				ExpiresAt:         time.Now().Add(time.Hour),
				CreatedAt:         time.Now(),
			}))

			// User with matching epoch but non-active status — the P1.3 attack window.
			user := mustBuildUserWithStatus(t, sub, epochAtIssue, tt.status)
			svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

			tok, err := IssueTestToken(testPrivKey, sub, nil, time.Hour, sid)
			require.NoError(t, err)

			_, verifyErr := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
			require.Error(t, verifyErr, "non-active user must be rejected even when epoch matches")

			var ec *errcode.Error
			require.ErrorAs(t, verifyErr, &ec)
			assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
				"must be KindUnauthenticated (uniform 401, not 403) —防枚举")
			assert.Equal(t, errcode.ErrAuthInvalidToken, ec.Code,
				"must be ErrAuthInvalidToken — same code as revoked-session / epoch-mismatch paths")
			assert.Contains(t, verifyErr.Error(), errMsgAuthFailed,
				"must return the uniform auth-failed message to prevent status enumeration")
			assert.NotContains(t, verifyErr.Error(), string(tt.status),
				"must NOT leak user status in the error message (防枚举)")
		})
	}
}

// TestEnforce_ActiveUser_EpochMatch_Allowed_P1_3b_Control is the control case
// for the P1.3b test: same setup (session exists, not revoked, epoch matches)
// but user is active — claims must be returned successfully.
func TestEnforce_ActiveUser_EpochMatch_Allowed_P1_3b_Control(t *testing.T) {
	const epochAtIssue = int64(3)

	store := newTestStore(t)
	sid := "sess-active-control"
	sub := "usr-active-control"

	require.NoError(t, store.Create(context.Background(), &session.Session{
		ID:                sid,
		SubjectID:         sub,
		JTI:               sid + "-jti",
		AuthzEpochAtIssue: epochAtIssue,
		ExpiresAt:         time.Now().Add(time.Hour),
		CreatedAt:         time.Now(),
	}))

	user := mustBuildUser(t, sub, epochAtIssue)
	svc := newSvcWithUserRepo(t, store, &stubUserRepo{user: user})

	tok, err := IssueTestToken(testPrivKey, sub, nil, time.Hour, sid)
	require.NoError(t, err)

	claims, verifyErr := svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, verifyErr, "active user with matching epoch must be accepted")
	assert.Equal(t, sub, claims.Subject)
}

// TestLogSessionLookupError_LogLevel verifies S40: IsDomainNotFound whitelist
// determines log level — only whitelisted domain not-found codes produce Warn;
// all infra, plain, or non-whitelisted domain errors produce Error.
//
// S4b Batch 3H update: infra errors from the session store now log at ERROR
// with message "session store unavailable" (infra branch in enforceSessionState),
// while non-whitelisted domain errors still go through logSessionLookupError
// and emit "session repo unavailable".
func TestLogSessionLookupError_LogLevel(t *testing.T) {
	tests := []struct {
		name          string
		storeErr      error
		wantLogLevel  slog.Level
		wantLogSubstr string // substring of the expected log message to search for
	}{
		{
			name:          "plain infra error logs at Error",
			storeErr:      fmt.Errorf("db connection timeout"),
			wantLogLevel:  slog.LevelError,
			wantLogSubstr: "session store unavailable",
		},
		{
			name: "errcode ErrSessionNotFound (domain, whitelist) logs at Warn",
			storeErr: errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found",
				errcode.WithCategory(errcode.CategoryDomain)),
			wantLogLevel:  slog.LevelWarn,
			wantLogSubstr: "session not found",
		},
		{
			name: "non-whitelisted errcode domain logs at Error",
			storeErr: errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound, "order not found",
				errcode.WithCategory(errcode.CategoryDomain)),
			wantLogLevel:  slog.LevelError,
			wantLogSubstr: "session repo unavailable",
		},
		{
			name:          "errcode with CategoryInfra logs at Error",
			storeErr:      errcode.New(errcode.KindInternal, errcode.ErrInternal, "db down"),
			wantLogLevel:  slog.LevelError,
			wantLogSubstr: "session store unavailable",
		},
		{
			name:          "errcode with CategoryUnspecified (zero) logs at Error (fail-closed)",
			storeErr:      errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "not found"),
			wantLogLevel:  slog.LevelError,
			wantLogSubstr: "session store unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			svc, svcErr := NewService(testVerifier, capturingStore{getErr: tt.storeErr}, &stubUserRepo{}, logger)
			require.NoError(t, svcErr)

			tok, err := IssueTestToken(testPrivKey, "usr-log", nil, time.Hour, "sess-log-test")
			require.NoError(t, err)

			_, _ = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)

			logOutput := buf.String()
			require.NotEmpty(t, logOutput, "expected at least one log line")

			// Locate the specific session-lookup log line by expected message substring.
			entry := sloghelper.FindLogEntry(logOutput, tt.wantLogSubstr)
			require.NotNil(t, entry,
				"expected a log line containing %q", tt.wantLogSubstr)

			wantLevel := "ERROR"
			if tt.wantLogLevel == slog.LevelWarn {
				wantLevel = "WARN"
				// Also confirm no spurious ERROR line for session store failures.
				errEntry := sloghelper.FindLogEntry(logOutput, "session store unavailable")
				assert.Nil(t, errEntry,
					"must not emit ERROR 'session store unavailable' when domain not-found whitelist matches")
			}
			assert.Equal(t, wantLevel, entry["level"],
				"log level mismatch for error: %v", tt.storeErr)
		})
	}
}
