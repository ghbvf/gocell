package sessionlogin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testKeySet, _, _ = auth.MustNewTestKeySet()
	testIssuer       *auth.JWTIssuer
)

func init() {
	var err error
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-access-core", auth.DefaultAccessTokenTTL,
		auth.WithDefaultAudience("gocell"))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

// TestNewService_InheritsAudienceFromIssuer verifies that NewService reads the
// default audience from the issuer's DefaultAudience() and uses it when minting
// tokens, so no external constant (like auth.DefaultJWTAudience) is required.
func TestNewService_InheritsAudienceFromIssuer(t *testing.T) {
	svc, userRepo := newTestService()
	seedUser(userRepo, "aud-user", "pass123")

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "aud-user", Password: "pass123"})
	require.NoError(t, err)

	// The access token must carry the audience from the issuer's DefaultAudience.
	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Contains(t, accessClaims.Audience, "gocell",
		"access token aud must be populated from issuer.DefaultAudience()")

	// The refresh token must also carry the audience.
	refreshClaims, err := verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentRefresh)
	require.NoError(t, err)
	assert.Contains(t, refreshClaims.Audience, "gocell",
		"refresh token aud must be populated from issuer.DefaultAudience()")
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

func TestService_Login_TokensContainSessionID(t *testing.T) {
	svc, userRepo := newTestService()
	seedUser(userRepo, "sid-user", "pass123")

	// Need a verifier to decode the tokens.
	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "sid-user", Password: "pass123"})
	require.NoError(t, err)

	// Access token must contain sid.
	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	sid := accessClaims.SessionID
	assert.NotEmpty(t, sid, "access token must contain sid claim")
	assert.True(t, strings.HasPrefix(sid, "sess-"), "sid must start with sess-")

	// Refresh token must contain same sid.
	refreshClaims, err := verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentRefresh)
	require.NoError(t, err)
	refreshSid := refreshClaims.SessionID
	assert.NotEmpty(t, refreshSid, "refresh token must contain sid claim")
	assert.Equal(t, sid, refreshSid, "both tokens must share the same session ID")
}

// failingPublisher returns an error on every Publish call.
type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

func TestLogin_PasswordResetRequiredFlagPropagated(t *testing.T) {
	svc, userRepo := newTestService()

	// Seed user with PasswordResetRequired=true.
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass123"), bcrypt.MinCost)
	user, _ := domain.NewUser("reset-user", "reset@test.com", string(hash))
	user.ID = "usr-reset"
	user.MarkPasswordResetRequired()
	_ = userRepo.Create(context.Background(), user)

	pair, err := svc.Login(context.Background(), LoginInput{Username: "reset-user", Password: "pass123"})
	require.NoError(t, err)

	// TokenPair flag must be true.
	assert.True(t, pair.PasswordResetRequired, "TokenPair.PasswordResetRequired must mirror user flag")

	// JWT claim must also be true.
	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, claims.PasswordResetRequired, "access token must carry password_reset_required=true claim")
}

func TestLogin_NoResetWhenFlagFalse(t *testing.T) {
	svc, userRepo := newTestService()
	seedUser(userRepo, "normal-user", "pass123")

	pair, err := svc.Login(context.Background(), LoginInput{Username: "normal-user", Password: "pass123"})
	require.NoError(t, err)

	assert.False(t, pair.PasswordResetRequired, "TokenPair.PasswordResetRequired must be false for normal user")

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.False(t, claims.PasswordResetRequired, "access token must not carry reset claim for normal user")
}

func TestService_IssueForUser(t *testing.T) {
	svc, userRepo := newTestService()
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
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	eb := eventbus.New()
	svc := NewService(userRepo, sessionRepo, roleRepo, eb, testIssuer, slog.Default())
	seedUser(userRepo, "issue-persist", "pass123")

	u, err := userRepo.GetByUsername(context.Background(), "issue-persist")
	require.NoError(t, err)

	pair, err := svc.IssueForUser(context.Background(), u.ID)
	require.NoError(t, err)
	require.NotEmpty(t, pair.SessionID)

	// The session must be findable by its ID so sessionvalidate does not fail.
	session, err := sessionRepo.GetByID(context.Background(), pair.SessionID)
	require.NoError(t, err, "session must be persisted after IssueForUser so sessionvalidate can look it up")
	assert.Equal(t, pair.SessionID, session.ID)
	assert.Equal(t, u.ID, session.UserID)
	assert.False(t, session.IsRevoked(), "newly issued session must not be revoked")
	assert.False(t, session.IsExpired(), "newly issued session must not be expired")
}

func TestService_Login_PublishError_DoesNotFailLogin(t *testing.T) {
	userRepo := mem.NewUserRepository()
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	seedUser(userRepo, "pub-err", "pass123")

	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	svc := NewService(userRepo, sessionRepo, roleRepo, fp, testIssuer, slog.Default())

	pair, err := svc.Login(context.Background(), LoginInput{Username: "pub-err", Password: "pass123"})
	require.NoError(t, err, "publish failure in demo mode should not fail login")
	assert.NotEmpty(t, pair.AccessToken)
}
