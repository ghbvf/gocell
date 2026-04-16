package sessionvalidate

import (
	"context"
	"fmt"
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

	// Seed an expired session.
	expiredSession := &domain.Session{
		ID:           "sess-expired",
		UserID:       "usr-3",
		AccessToken:  "dummy3",
		RefreshToken: "dummy-refresh3",
		ExpiresAt:    time.Now().Add(-time.Hour), // already expired
		CreatedAt:    time.Now().Add(-2 * time.Hour),
	}
	require.NoError(t, sessionRepo.Create(context.Background(), expiredSession))

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
			name: "token with expired session",
			token: func() string {
				tok, _ := IssueTestToken(testPrivKey, "usr-3", nil, time.Hour, "sess-expired")
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
				if tt.name == "valid token with active session" {
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

// errorSessionRepo simulates infrastructure failures (DB timeout, connection reset).
type errorSessionRepo struct{}

func (errorSessionRepo) Create(_ context.Context, _ *domain.Session) error { return nil }
func (errorSessionRepo) GetByID(_ context.Context, _ string) (*domain.Session, error) {
	return nil, fmt.Errorf("db connection timeout")
}
func (errorSessionRepo) GetByRefreshToken(_ context.Context, _ string) (*domain.Session, error) {
	return nil, nil
}
func (errorSessionRepo) GetByPreviousRefreshToken(_ context.Context, _ string) (*domain.Session, error) {
	return nil, nil
}
func (errorSessionRepo) Update(_ context.Context, _ *domain.Session) error { return nil }
func (errorSessionRepo) Delete(_ context.Context, _ string) error          { return nil }
func (errorSessionRepo) RevokeByUserID(_ context.Context, _ string) error  { return nil }

func TestService_Verify_DBError_FailsClosed(t *testing.T) {
	// Infrastructure errors (not just "not found") must also fail-closed.
	svc := NewService(testVerifier, errorSessionRepo{}, slog.Default())

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour, "sess-db-fail")
	require.NoError(t, err)

	_, err = svc.Verify(context.Background(), tok)
	require.Error(t, err, "DB errors must cause verification failure (fail-closed)")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN")
}

func TestService_Verify_NilSessionRepo_NoSid(t *testing.T) {
	// When sessionRepo is nil (demo mode), tokens without sid are accepted.
	svc := NewService(testVerifier, nil, slog.Default())

	tok, err := IssueTestToken(testPrivKey, "usr-1", nil, time.Hour)
	require.NoError(t, err)

	claims, err := svc.Verify(context.Background(), tok)
	require.NoError(t, err)
	assert.Equal(t, "usr-1", claims.Subject)
}
