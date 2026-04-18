package sessionrefresh

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
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
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-access-core", auth.DefaultAccessTokenTTL)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	testVerifier, err = auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

func newTestService() (*Service, *mem.SessionRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	return NewService(sessionRepo, roleRepo, testIssuer, testVerifier, slog.Default()), sessionRepo
}

func newTestServiceWithUserRepo() (*Service, *mem.SessionRepository, *mem.UserRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	svc := NewService(sessionRepo, roleRepo, testIssuer, testVerifier, slog.Default(),
		WithUserRepository(userRepo))
	return svc, sessionRepo, userRepo
}

func issueTestToken(sub string) string {
	tok, _ := testIssuer.Issue(auth.TokenIntentRefresh, sub, auth.IssueOptions{
		Audience: []string{auth.DefaultJWTAudience},
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
			svc, repo := newTestService()
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
	svc, repo := newTestService()

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
	svc, _ := newTestService()

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
	svc, repo := newTestService()

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
	svc, repo := newTestService()

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
	svc := NewService(sessionRepo, roleRepo, testIssuer, testVerifier, slog.Default())

	// Issue a token with sid claim to tie to a session.
	rt, err := testIssuer.Issue(auth.TokenIntentRefresh, "usr-sa", auth.IssueOptions{
		Audience:  []string{auth.DefaultJWTAudience},
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
