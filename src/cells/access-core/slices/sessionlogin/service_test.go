package sessionlogin

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testKeySet, testPrivKey, _ = auth.MustNewTestKeySet()
	testIssuer                 *auth.JWTIssuer
)

func init() {
	var err error
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-access-core", 15*time.Minute)
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

func newTestService() (*Service, *mem.UserRepository) {
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	eb := eventbus.New()
	return NewService(userRepo, sessionRepo, roleRepo, eb, testIssuer, slog.Default()), userRepo
}

// seedUser creates a user with a bcrypt-hashed password.
func seedUser(repo *mem.UserRepository, username, password string) {
	hash, _ := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	user, _ := domain.NewUser(username, username+"@test.com", string(hash))
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
				u.Lock()
				_ = r.Update(context.Background(), u)
			},
			input:   LoginInput{Username: "locked", Password: "pass"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, userRepo := newTestService()
			tt.setup(userRepo)

			pair, err := svc.Login(context.Background(), tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, pair)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, pair.AccessToken)
				assert.NotEmpty(t, pair.RefreshToken)
				assert.False(t, pair.ExpiresAt.IsZero())
			}
		})
	}
}
