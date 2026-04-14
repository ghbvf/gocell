package sessionrefresh

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testKeySet, testPrivKey, _ = auth.MustNewTestKeySet()
	testIssuer                 *auth.JWTIssuer
	testVerifier               *auth.JWTVerifier
)

func init() {
	var err error
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-access-core", 15*time.Minute)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	testVerifier, err = auth.NewJWTVerifier(testKeySet)
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

func newTestService() (*Service, *mem.SessionRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	return NewService(sessionRepo, roleRepo, testIssuer, testVerifier, slog.Default()), sessionRepo
}

func issueTestToken(sub string) string {
	tok, _ := testIssuer.Issue(sub, nil, nil)
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
	tokenStr, _ := otherIssuer.Issue("usr-1", nil, nil)

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

	// With optimistic locking, at most 1 goroutine succeeds.
	// Others fail with version conflict or reuse detection.
	assert.LessOrEqual(t, successes, int64(1),
		"at most one concurrent refresh should succeed")
	assert.GreaterOrEqual(t, failures, int64(goroutines-1),
		"remaining goroutines should fail")
}
