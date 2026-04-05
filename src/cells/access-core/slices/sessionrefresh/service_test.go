package sessionrefresh

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testKey = []byte("test-signing-key-32bytes-long!!!!")

func newTestService() (*Service, *mem.SessionRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	return NewService(sessionRepo, roleRepo, testKey, slog.Default()), sessionRepo
}

func issueTestToken(sub string) string {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": sub,
		"exp": jwt.NewNumericDate(time.Now().Add(time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
	})
	s, _ := token.SignedString(testKey)
	return s
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
