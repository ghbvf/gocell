package sessionvalidate

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
	testKeySet, testPrivKey, _ = auth.MustNewTestKeySet()
	testVerifier               *auth.JWTVerifier
)

func init() {
	var err error
	testVerifier, err = auth.NewJWTVerifier(testKeySet)
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

func TestService_Verify(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()

	// Seed an active session for revocation tests.
	activeSession := &domain.Session{
		ID:           "sess-active",
		UserID:       "usr-1",
		AccessToken:  "dummy",
		RefreshToken: "dummy-refresh",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
	}
	require.NoError(t, sessionRepo.Create(context.Background(), activeSession))

	// Seed a revoked session.
	revokedSession := &domain.Session{
		ID:           "sess-revoked",
		UserID:       "usr-2",
		AccessToken:  "dummy2",
		RefreshToken: "dummy-refresh2",
		ExpiresAt:    time.Now().Add(time.Hour),
		CreatedAt:    time.Now(),
	}
	revokedSession.Revoke()
	require.NoError(t, sessionRepo.Create(context.Background(), revokedSession))

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
			wantErr: false,
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

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(testVerifier, sessionRepo, slog.Default())

			claims, err := svc.Verify(context.Background(), tt.token())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantSub, claims.Subject)
				if tt.name == "valid token without sid" || tt.name == "valid token with active session" {
					assert.Contains(t, claims.Roles, "admin")
				}
				assert.Equal(t, "gocell-access-core", claims.Issuer)
			}
		})
	}
}

func TestService_Verify_NilSessionRepo(t *testing.T) {
	// When sessionRepo is nil (backward compatibility), sid claim is ignored.
	svc := NewService(testVerifier, nil, slog.Default())

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-any")
	require.NoError(t, err)

	claims, err := svc.Verify(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "usr-1", claims.Subject)
}
