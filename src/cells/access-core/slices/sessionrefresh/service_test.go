package sessionrefresh

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testPrivKey, testPubKey = auth.MustGenerateTestKeyPair()
	testIssuer              = auth.NewJWTIssuer(testPrivKey, "gocell-access-core", 15*time.Minute)
	testVerifier            = auth.NewJWTVerifier(testPubKey)
)

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
	otherPriv, _ := auth.MustGenerateTestKeyPair()
	otherIssuer := auth.NewJWTIssuer(otherPriv, "gocell-access-core", time.Hour)
	tokenStr, _ := otherIssuer.Issue("usr-1", nil, nil)

	_, err := svc.Refresh(context.Background(), tokenStr)
	assert.Error(t, err, "should reject token signed with a different key")
}
