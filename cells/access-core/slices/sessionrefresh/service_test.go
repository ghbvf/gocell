package sessionrefresh

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testKeySet, _, _ = auth.MustNewTestKeySet()
	testIssuer       *auth.JWTIssuer
	testVerifier     *auth.JWTVerifier
)

func init() {
	var err error
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-access-core", auth.DefaultAccessTokenTTL,
		auth.WithDefaultAudience("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
	testVerifier, err = auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

// TestNewService_InheritsAudienceFromIssuer_Refresh verifies that the sessionrefresh
// Service reads the default audience from issuer.DefaultAudience() and uses it when
// minting new tokens during rotation, without relying on a hard-coded constant.
func TestNewService_InheritsAudienceFromIssuer_Refresh(t *testing.T) {
	svc, sessionRepo := newTestService("usr-aud-refresh")

	rt := issueTestToken("usr-aud-refresh")
	sess, err := domain.NewSession("usr-aud-refresh", "at", rt, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-aud-refresh"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), rt)
	require.NoError(t, err)

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Contains(t, accessClaims.Audience, "gocell",
		"rotated access token aud must come from issuer.DefaultAudience()")

	refreshClaims, err := verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentRefresh)
	require.NoError(t, err)
	assert.Contains(t, refreshClaims.Audience, "gocell",
		"rotated refresh token aud must come from issuer.DefaultAudience()")
}

// newTestService creates a refresh service with a minimal in-memory userRepo
// (P1-11: userRepo is a required positional parameter in NewService).
//
// seedUsers lists user IDs to pre-populate so GetByID succeeds. Since the
// refresh fail-closed policy (F1) aborts refresh when userRepo.GetByID errors,
// tests that exercise a successful refresh must seed the session's user.
func newTestService(seedUsers ...string) (*Service, *mem.SessionRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	for _, uid := range seedUsers {
		u, _ := domain.NewUser(uid, uid+"@test.local", "hash")
		u.ID = uid
		_ = userRepo.Create(context.Background(), u)
	}
	return NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, slog.Default()), sessionRepo
}

// newTestServiceWithUserRepo creates a service and returns the userRepo for
// tests that need to seed user fixtures and assert on the PasswordResetRequired flag.
func newTestServiceWithUserRepo() (*Service, *mem.SessionRepository, *mem.UserRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	svc := NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, slog.Default())
	return svc, sessionRepo, userRepo
}

func issueTestToken(sub string) string {
	tok, _ := testIssuer.Issue(auth.TokenIntentRefresh, sub, auth.IssueOptions{
		Audience: []string{"gocell"},
	})
	return tok
}

func TestService_Refresh(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mem.SessionRepository) string // returns refresh token
		wantErr bool
	}{
		{
			name: "valid refresh",
			setup: func(repo *mem.SessionRepository) string {
				rt := issueTestToken("usr-1")
				sess, _ := domain.NewSession("usr-1", "at", rt, time.Now().Add(time.Hour))
				sess.ID = "sess-1"
				_ = repo.Create(context.Background(), sess)
				return rt
			},
			wantErr: false,
		},
		{
			name: "revoked session",
			setup: func(repo *mem.SessionRepository) string {
				rt := issueTestToken("usr-2")
				sess, _ := domain.NewSession("usr-2", "at", rt, time.Now().Add(time.Hour))
				sess.ID = "sess-2"
				sess.Revoke()
				_ = repo.Create(context.Background(), sess)
				return rt
			},
			wantErr: true,
		},
		{
			name:    "empty token",
			setup:   func(_ *mem.SessionRepository) string { return "" },
			wantErr: true,
		},
		{
			name:    "invalid JWT",
			setup:   func(_ *mem.SessionRepository) string { return "bad-token" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService("usr-1", "usr-2")
			refreshToken := tt.setup(repo)

			pair, err := svc.Refresh(context.Background(), refreshToken)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, pair)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, pair.AccessToken)
				assert.NotEmpty(t, pair.RefreshToken)
			}
		})
	}
}

func TestService_Refresh_TokenRotation(t *testing.T) {
	svc, repo := newTestService("usr-rot")

	// Create a session with a known refresh token.
	rt1 := issueTestToken("usr-rot")
	sess, err := domain.NewSession("usr-rot", "at", rt1, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-rot"
	require.NoError(t, repo.Create(context.Background(), sess))

	// First refresh should succeed and rotate the token.
	pair1, err := svc.Refresh(context.Background(), rt1)
	require.NoError(t, err)
	assert.NotEqual(t, rt1, pair1.RefreshToken, "refresh token should be rotated")

	// The old token should no longer work for a normal refresh (session not found by that token).
	// But it should be detected as reuse and revoke the session.
	_, err = svc.Refresh(context.Background(), rt1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reuse")

	// The session should now be revoked.
	revokedSess, err := repo.GetByID(context.Background(), "sess-rot")
	require.NoError(t, err)
	assert.True(t, revokedSess.IsRevoked(), "session should be revoked after token reuse detection")

	// Even the new token should fail because the session is revoked.
	_, err = svc.Refresh(context.Background(), pair1.RefreshToken)
	require.Error(t, err)
}

func TestService_Refresh_SigningMethodCheck(t *testing.T) {
	svc, _ := newTestService("usr-1")

	// Tokens signed with a different key should be rejected by the verifier.
	otherPriv, otherPub := auth.MustGenerateTestKeyPair()
	otherKS, err := auth.NewKeySet(otherPriv, otherPub)
	require.NoError(t, err)
	otherIssuer, err := auth.NewJWTIssuer(otherKS, "gocell-access-core", time.Hour)
	require.NoError(t, err)
	tokenStr, _ := otherIssuer.Issue(auth.TokenIntentRefresh, "usr-1", auth.IssueOptions{})

	_, err = svc.Refresh(context.Background(), tokenStr)
	assert.Error(t, err, "should reject token signed with a different key")
}

// TestService_Refresh_ConcurrentRefresh verifies that concurrent refresh
// attempts on the same session result in at most one success. The remaining
// goroutines either get a version conflict (409) or trigger reuse detection.
// Run with -race to verify memory safety.
func TestService_Refresh_ConcurrentRefresh(t *testing.T) {
	svc, repo := newTestService("usr-conc")

	rt := issueTestToken("usr-conc")
	sess, err := domain.NewSession("usr-conc", "at", rt, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-conc"
	require.NoError(t, repo.Create(context.Background(), sess))

	const goroutines = 5
	var (
		wg        sync.WaitGroup
		successes int64
		failures  int64
		mu        sync.Mutex
	)

	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, refreshErr := svc.Refresh(context.Background(), rt)
			mu.Lock()
			defer mu.Unlock()
			if refreshErr == nil {
				successes++
			} else {
				failures++
			}
		}()
	}

	wg.Wait()

	// With optimistic locking, exactly 1 goroutine succeeds.
	// Others fail with version conflict or reuse detection.
	assert.Equal(t, int64(1), successes,
		"exactly one concurrent refresh should succeed")
	assert.Equal(t, int64(goroutines-1), failures,
		"remaining goroutines should fail")
}

func TestService_Refresh_NewTokensContainSessionID(t *testing.T) {
	svc, repo := newTestService("usr-sid")

	rt := issueTestToken("usr-sid")
	sess, err := domain.NewSession("usr-sid", "at", rt, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-r1"
	require.NoError(t, repo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), rt)
	require.NoError(t, err)

	// Decode the new access token to verify sid.
	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "sess-r1", accessClaims.SessionID, "new access token must carry the session ID")

	refreshClaims, err := verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentRefresh)
	require.NoError(t, err)
	assert.Equal(t, "sess-r1", refreshClaims.SessionID, "new refresh token must carry the session ID")
}

// TestService_Refresh_SessionAwareVerifier proves that sessionrefresh still
// catches revoked sessions even when wired with the raw JWTVerifier (the
// production wiring since PR-P0-AUTH-INTENT dropped the sessionvalidate-based
// verifier, which hard-requires token_use=access and cannot validate refresh
// tokens). Revocation is now enforced by the refresh service's own
// sessionRepo lookup + Session.IsRevoked check.
func TestService_Refresh_SessionAwareVerifier(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()

	// Wire refresh service with the intent-aware JWT verifier (production path).
	userRepo := mem.NewUserRepository()
	seedUser, _ := domain.NewUser("usr-sa", "usr-sa@test.local", "hash")
	seedUser.ID = "usr-sa"
	require.NoError(t, userRepo.Create(context.Background(), seedUser))
	svc := NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, slog.Default())

	// Issue a token with sid claim to tie to a session.
	rt, err := testIssuer.Issue(auth.TokenIntentRefresh, "usr-sa", auth.IssueOptions{
		Audience:  []string{"gocell"},
		SessionID: "sess-sa",
	})
	require.NoError(t, err)

	sess, err := domain.NewSession("usr-sa", "at", rt, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-sa"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	// Normal refresh should succeed.
	pair, err := svc.Refresh(context.Background(), rt)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)

	// Now revoke the session externally.
	sess, err = sessionRepo.GetByID(context.Background(), "sess-sa")
	require.NoError(t, err)
	sess.Revoke()
	require.NoError(t, sessionRepo.Update(context.Background(), sess))

	// Attempt refresh with the new (rotated) token — the session-aware verifier
	// should reject it at the Verify() step because the session is revoked.
	_, err = svc.Refresh(context.Background(), pair.RefreshToken)
	assert.Error(t, err, "session-aware verifier should reject revoked session at Verify step")
}

// TestRefresh_FailClosedWhenUserUnavailable verifies the F1 fail-closed policy:
// when userRepo.GetByID returns an error (user deleted mid-session, or transient
// DB failure), refresh must return ErrAuthRefreshFailed rather than signing a
// new access token that omits the password_reset_required claim. Returning a
// default value here would let an attacker bypass the reset gate during a DB
// blip.
func TestRefresh_FailClosedWhenUserUnavailable(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository() // intentionally empty — GetByID returns error
	svc := NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, slog.Default())

	rt := issueTestToken("usr-missing")
	sess, err := domain.NewSession("usr-missing", "at", rt, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-missing"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), rt)
	require.Error(t, err, "fail-closed: refresh must error when user is unavailable")
	assert.Nil(t, pair)
}

// TestRefresh_FlagPropagatesFromCurrentUser_AfterClear ensures that after a user
// clears PasswordResetRequired (e.g. via ChangePassword), the next refresh
// produces a new access token with password_reset_required=false.
func TestRefresh_FlagPropagatesFromCurrentUser_AfterClear(t *testing.T) {
	svc, sessionRepo, userRepo := newTestServiceWithUserRepo()

	// Seed a user with reset flag = false (already cleared).
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("ref-user-clear", "ref-clear@test.com", string(hash))
	user.ID = "usr-ref-clear"
	// PasswordResetRequired is false by default.
	require.NoError(t, userRepo.Create(context.Background(), user))

	rt := issueTestToken("usr-ref-clear")
	sess, _ := domain.NewSession("usr-ref-clear", "at", rt, time.Now().Add(time.Hour))
	sess.ID = "sess-ref-clear"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), rt)
	require.NoError(t, err)
	assert.False(t, pair.PasswordResetRequired, "after clearing flag, refreshed token must have claim=false")

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.False(t, claims.PasswordResetRequired, "access token claim must be false after flag cleared")
}

// infraSessionRepo is a session repo stub whose GetByRefreshToken returns an
// infra error (e.g. db timeout). Used to test P1-18: infra errors must not
// enter the reuse detection branch.
type infraSessionRepo struct {
	mem.SessionRepository
	infraErr error
}

func newInfraSessionRepo(infraErr error) *infraSessionRepo {
	return &infraSessionRepo{
		SessionRepository: *mem.NewSessionRepository(),
		infraErr:          infraErr,
	}
}

func (r *infraSessionRepo) GetByRefreshToken(_ context.Context, _ string) (*domain.Session, error) {
	return nil, r.infraErr
}

// notFoundSessionRepo is a session repo stub whose GetByRefreshToken returns
// a domain ErrSessionNotFound (expected reuse-detection path).
type notFoundSessionRepo struct {
	mem.SessionRepository
}

func (r *notFoundSessionRepo) GetByRefreshToken(_ context.Context, _ string) (*domain.Session, error) {
	return nil, errcode.NewDomain(errcode.ErrSessionNotFound, "session not found")
}

// TestLookupSession_InfraError_DoesNotEnterReuseBranch verifies P1-18:
// when GetByRefreshToken returns an infra error (plain error or CategoryInfra),
// lookupSession must NOT proceed to GetByPreviousRefreshToken (reuse branch).
// It must return an error and log at slog.Error.
func TestLookupSession_InfraError_DoesNotEnterReuseBranch(t *testing.T) {
	infraErrors := []struct {
		name string
		err  error
	}{
		{"plain DB timeout", fmt.Errorf("db connection timeout")},
		{"CategoryInfra errcode", errcode.NewInfra(errcode.ErrInternal, "storage unavailable")},
	}

	for _, tc := range infraErrors {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			sessionRepo := newInfraSessionRepo(tc.err)
			roleRepo := mem.NewRoleRepository()
			userRepo := mem.NewUserRepository()
			svc := NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, logger)

			rt := issueTestToken("usr-infra")

			pair, err := svc.Refresh(context.Background(), rt)
			require.Error(t, err, "infra error must cause Refresh to fail")
			assert.Nil(t, pair)

			logOutput := buf.String()
			// P1-4: use precise JSON-line matching to avoid false positives from
			// other log lines during the request lifecycle.
			entry := sloghelper.FindLogEntry(logOutput, "infra error on session lookup")
			require.NotNil(t, entry,
				"expected a log line containing 'infra error on session lookup'")
			assert.Equal(t, "ERROR", entry["level"],
				"infra lookup error must be logged at ERROR")
			// Confirm the reuse-detection path was not entered.
			reuseEntry := sloghelper.FindLogEntry(logOutput, "refresh token reuse detected")
			assert.Nil(t, reuseEntry,
				"infra error must not be logged as token reuse")
		})
	}
}

// TestLookupSession_DomainNotFound_EntersReuseBranch verifies that a domain
// ErrSessionNotFound still enters the reuse detection branch (preserved
// existing behaviour).
func TestLookupSession_DomainNotFound_EntersReuseBranch(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	// notFoundSessionRepo returns domain ErrSessionNotFound on primary lookup
	// and nil error on GetByPreviousRefreshToken (simulating no reuse found).
	sessionRepo := &notFoundSessionRepo{
		SessionRepository: *mem.NewSessionRepository(),
	}
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	svc := NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, logger)

	rt := issueTestToken("usr-notfound")

	// GetByPreviousRefreshToken will also not find it → returns ErrAuthRefreshFailed
	_, err := svc.Refresh(context.Background(), rt)
	require.Error(t, err, "session not found must still fail refresh")
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED")
}

// TestRefresh_FlagStillSetWhenUserNotChanged ensures that a user who has not
// changed their password keeps getting tokens with password_reset_required=true
// on each refresh.
func TestRefresh_FlagStillSetWhenUserNotChanged(t *testing.T) {
	svc, sessionRepo, userRepo := newTestServiceWithUserRepo()

	// Seed a user with reset flag = true (bootstrap user who hasn't changed password yet).
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("ref-user-reset", "ref-reset@test.com", string(hash))
	user.ID = "usr-ref-reset"
	user.MarkPasswordResetRequired()
	require.NoError(t, userRepo.Create(context.Background(), user))

	rt := issueTestToken("usr-ref-reset")
	sess, _ := domain.NewSession("usr-ref-reset", "at", rt, time.Now().Add(time.Hour))
	sess.ID = "sess-ref-reset"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), rt)
	require.NoError(t, err)
	assert.True(t, pair.PasswordResetRequired, "refreshed token must still have claim=true when user hasn't changed password")

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, claims.PasswordResetRequired, "access token claim must be true when flag not cleared")
}

// notFoundThenInfraSessionRepo is a session repo stub used in P1-2 tests.
// GetByRefreshToken returns domain ErrSessionNotFound (triggers reuse branch),
// GetByPreviousRefreshToken returns an infra error.
type notFoundThenInfraSessionRepo struct {
	mem.SessionRepository
	reuseErr error
}

func (r *notFoundThenInfraSessionRepo) GetByRefreshToken(_ context.Context, _ string) (*domain.Session, error) {
	return nil, errcode.NewDomain(errcode.ErrSessionNotFound, "session not found")
}

func (r *notFoundThenInfraSessionRepo) GetByPreviousRefreshToken(_ context.Context, _ string) (*domain.Session, error) {
	return nil, r.reuseErr
}

// TestLookupSession_ReuseErr_InfraError_LogsAtError verifies P1-2:
// when GetByRefreshToken returns domain not-found (enters reuse branch) and
// GetByPreviousRefreshToken returns an infra error, lookupSession must log at
// slog.Error so ops dashboards surface the storage outage.
func TestLookupSession_ReuseErr_InfraError_LogsAtError(t *testing.T) {
	infraReuseErrs := []struct {
		name string
		err  error
	}{
		{"plain DB timeout on reuse lookup", fmt.Errorf("db connection timeout")},
		{"CategoryInfra on reuse lookup", errcode.NewInfra(errcode.ErrInternal, "storage unavailable")},
	}

	for _, tc := range infraReuseErrs {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

			sessionRepo := &notFoundThenInfraSessionRepo{
				SessionRepository: *mem.NewSessionRepository(),
				reuseErr:          tc.err,
			}
			roleRepo := mem.NewRoleRepository()
			userRepo := mem.NewUserRepository()
			svc := NewService(sessionRepo, roleRepo, userRepo, testIssuer, testVerifier, logger)

			rt := issueTestToken("usr-reuseinfra")

			pair, err := svc.Refresh(context.Background(), rt)
			require.Error(t, err, "infra error on reuse lookup must cause Refresh to fail")
			assert.Nil(t, pair)

			logOutput := buf.String()
			assert.Contains(t, logOutput, `"level":"ERROR"`,
				"infra error on reuse lookup must be logged at ERROR")
		})
	}
}
