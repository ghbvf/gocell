package sessionrefresh

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testKeySet, _, _ = auth.MustNewTestKeySet()
	testIssuer       *auth.JWTIssuer
)

func init() {
	var err error
	// Issuer is constructed with a default audience via WithIssuerAudiencesFromSlice
	// (Registry path). The slice service no longer caches audience separately (S31).
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-accesscore", auth.DefaultAccessTokenTTL,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

func newTestRefreshStore() refresh.Store {
	clock := storetest.NewFakeClock(time.Now())
	return refreshmem.New(refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: time.Hour}, clock, nil)
}

// TestNewService_IssuerDefaultAudienceWrittenOnRefresh verifies that the
// sessionrefresh Service issues tokens with the audience configured in the
// issuer (Registry path), without caching audience separately (S31).
func TestNewService_IssuerDefaultAudienceWrittenOnRefresh(t *testing.T) {
	svc, sessionRepo, refreshStore := newTestServiceWithRefreshStore("usr-aud-refresh")

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-aud-refresh", "usr-aud-refresh")
	require.NoError(t, err)

	sess, err := domain.NewSession("usr-aud-refresh", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-aud-refresh"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Contains(t, accessClaims.Audience, "gocell",
		"rotated access token aud must come from issuer default audience (Registry)")
}

// newTestService creates a refresh service with a minimal in-memory userRepo.
// seedUsers lists user IDs to pre-populate so GetByID succeeds.
func newTestService(seedUsers ...string) (*Service, *mem.SessionRepository) {
	svc, sessionRepo, _ := newTestServiceWithRefreshStore(seedUsers...)
	return svc, sessionRepo
}

// newTestServiceWithRefreshStore creates a service and exposes the refreshStore
// for tests that need to issue wire tokens via the store directly.
func newTestServiceWithRefreshStore(seedUsers ...string) (*Service, *mem.SessionRepository, refresh.Store) {
	svc, sessionRepo, refreshStore, _ := newTestServiceWithClock(seedUsers...)
	return svc, sessionRepo, refreshStore
}

// newTestServiceWithClock creates a service and exposes both the refreshStore
// and the underlying FakeClock for tests that need to advance time (e.g. to
// move past the ReuseInterval so old tokens are rejected rather than grace-retried).
func newTestServiceWithClock(seedUsers ...string) (*Service, *mem.SessionRepository, refresh.Store, *storetest.FakeClock) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	for _, uid := range seedUsers {
		u, _ := domain.NewUser(uid, uid+"@test.local", "hash")
		u.ID = uid
		_ = userRepo.Create(context.Background(), u)
	}
	clock := storetest.NewFakeClock(time.Now())
	refreshStore := refreshmem.New(refresh.Policy{ReuseInterval: 2 * time.Second, MaxAge: time.Hour}, clock, nil)
	svc := NewService(sessionRepo, roleRepo, userRepo, refreshStore, testIssuer, slog.Default())
	return svc, sessionRepo, refreshStore, clock
}

// newTestServiceWithUserRepo creates a service and returns the userRepo for
// tests that need to seed user fixtures and assert on the PasswordResetRequired flag.
func newTestServiceWithUserRepo() (*Service, *mem.SessionRepository, *mem.UserRepository) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	refreshStore := newTestRefreshStore()
	svc := NewService(sessionRepo, roleRepo, userRepo, refreshStore, testIssuer, slog.Default())
	return svc, sessionRepo, userRepo
}

// issueTestWireToken creates a session + issues a wire token from the refreshStore.
// Returns (svc, sessionRepo, wireToken).
func issueTestWireToken(t *testing.T, userID, sessionID string) (*Service, *mem.SessionRepository, refresh.Store, string) {
	t.Helper()
	svc, sessionRepo, refreshStore := newTestServiceWithRefreshStore(userID)

	sess, err := domain.NewSession(userID, "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = sessionID
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), sessionID, userID)
	require.NoError(t, err)

	return svc, sessionRepo, refreshStore, wireToken
}

// brokenRoleRepo simulates a RoleRepository outage for fail-closed tests.
type brokenRoleRepo struct {
	mem.RoleRepository
	err error
}

func (b *brokenRoleRepo) GetByUserID(_ context.Context, _ string) ([]*domain.Role, error) {
	return nil, b.err
}

// countingSessionRepo wraps mem.SessionRepository so tests can assert that
// Update was never called when sessionmint fails fast.
type countingSessionRepo struct {
	*mem.SessionRepository
	updates int
}

func (c *countingSessionRepo) Update(ctx context.Context, s *domain.Session) error {
	c.updates++
	return c.SessionRepository.Update(ctx, s)
}

// TestService_Refresh_RoleFetchFailure_AbortsRefresh asserts that when the
// RoleRepository is unavailable, Refresh fails with ErrAuthRoleFetchFailed
// and does NOT persist the rotated session — the fail-closed contract of
// PR-A7 / sessionmint: never issue a silently-degraded token.
func TestService_Refresh_RoleFetchFailure_AbortsRefresh(t *testing.T) {
	sessionRepo := &countingSessionRepo{SessionRepository: mem.NewSessionRepository()}
	roleRepo := &brokenRoleRepo{err: fmt.Errorf("roleRepo outage")}
	userRepo := mem.NewUserRepository()
	u, _ := domain.NewUser("usr-rolefail", "rolefail@test.local", "hash")
	u.ID = "usr-rolefail"
	require.NoError(t, userRepo.Create(context.Background(), u))

	refreshStore := newTestRefreshStore()
	svc := NewService(sessionRepo, roleRepo, userRepo, refreshStore, testIssuer, slog.Default())

	sess, err := domain.NewSession("usr-rolefail", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-rolefail"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-rolefail", "usr-rolefail")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "Refresh must fail when role fetch fails")
	assert.Nil(t, pair, "no token pair on failure")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code,
		"fail-closed: role fetch failure surfaces as ErrAuthRoleFetchFailed")

	assert.Equal(t, 0, sessionRepo.updates, "session must not be updated on fail-closed")
}

func TestService_Refresh(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*mem.SessionRepository, refresh.Store) string // returns wire token
		wantErr bool
	}{
		{
			name: "valid refresh",
			setup: func(repo *mem.SessionRepository, rs refresh.Store) string {
				sess, _ := domain.NewSession("usr-1", "at", time.Now().Add(time.Hour))
				sess.ID = "sess-1"
				_ = repo.Create(context.Background(), sess)
				wire, _, _ := rs.Issue(context.Background(), "sess-1", "usr-1")
				return wire
			},
			wantErr: false,
		},
		{
			name: "revoked session",
			setup: func(repo *mem.SessionRepository, rs refresh.Store) string {
				sess, _ := domain.NewSession("usr-2", "at", time.Now().Add(time.Hour))
				sess.ID = "sess-2"
				sess.Revoke()
				_ = repo.Create(context.Background(), sess)
				wire, _, _ := rs.Issue(context.Background(), "sess-2", "usr-2")
				return wire
			},
			wantErr: true,
		},
		{
			name:    "empty token",
			setup:   func(_ *mem.SessionRepository, _ refresh.Store) string { return "" },
			wantErr: true,
		},
		{
			name:    "invalid opaque token",
			setup:   func(_ *mem.SessionRepository, _ refresh.Store) string { return "bad-token" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, refreshStore := newTestServiceWithRefreshStore("usr-1", "usr-2")
			wireToken := tt.setup(repo, refreshStore)

			pair, err := svc.Refresh(context.Background(), wireToken)
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
	svc, repo, refreshStore, clock := newTestServiceWithClock("usr-rot")

	// Create a session and issue a wire token.
	sess, err := domain.NewSession("usr-rot", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-rot"
	require.NoError(t, repo.Create(context.Background(), sess))

	wire1, _, err := refreshStore.Issue(context.Background(), "sess-rot", "usr-rot")
	require.NoError(t, err)

	// First refresh should succeed and rotate the token.
	pair1, err := svc.Refresh(context.Background(), wire1)
	require.NoError(t, err)
	assert.NotEqual(t, wire1, pair1.RefreshToken, "refresh token should be rotated")

	// Advance the clock past the ReuseInterval (2s) so the old token is no longer
	// in the grace window and will be rejected as a reuse attack.
	clock.Advance(3 * time.Second)

	// Presenting the old wire token again should be rejected (reuse after grace).
	_, err = svc.Refresh(context.Background(), wire1)
	require.Error(t, err, "old wire token must be rejected after rotation")
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED")
}

func TestService_Refresh_ConcurrentRefresh(t *testing.T) {
	svc, repo, refreshStore := newTestServiceWithRefreshStore("usr-conc")

	sess, err := domain.NewSession("usr-conc", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-conc"
	require.NoError(t, repo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-conc", "usr-conc")
	require.NoError(t, err)

	const goroutines = 5
	type result struct {
		pair *TokenPair
		err  error
	}
	results := make(chan result, goroutines)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, refreshErr := svc.Refresh(context.Background(), wireToken)
			results <- result{p, refreshErr}
		}()
	}
	wg.Wait()
	close(results)

	// With the opaque store's grace-retry semantics, all goroutines presenting
	// the same wire token within the ReuseInterval window succeed and each
	// receives a distinct child token. This mirrors the storetest T10 contract.
	var successes int
	distinct := make(map[string]struct{})
	for r := range results {
		if r.err != nil {
			t.Logf("goroutine failed (expected if outside grace window): %v", r.err)
			continue
		}
		successes++
		if r.pair != nil {
			distinct[r.pair.RefreshToken] = struct{}{}
		}
	}

	require.Greater(t, successes, 0, "at least one concurrent refresh must succeed")
	assert.Len(t, distinct, successes, "each successful refresh must yield a distinct new wire token")
}

func TestService_Refresh_NewTokensContainSessionID(t *testing.T) {
	svc, repo, refreshStore := newTestServiceWithRefreshStore("usr-sid")

	sess, err := domain.NewSession("usr-sid", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-r1"
	require.NoError(t, repo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-r1", "usr-sid")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)

	// Decode the new access token to verify sid.
	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "sess-r1", accessClaims.SessionID, "new access token must carry the session ID")
}

// TestService_Refresh_SessionAwareVerifier proves that sessionrefresh catches
// revoked sessions even when the session is revoked out-of-band after the
// wire token is issued.
func TestService_Refresh_SessionAwareVerifier(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()
	seedUser, _ := domain.NewUser("usr-sa", "usr-sa@test.local", "hash")
	seedUser.ID = "usr-sa"
	require.NoError(t, userRepo.Create(context.Background(), seedUser))

	refreshStore := newTestRefreshStore()
	svc := NewService(sessionRepo, roleRepo, userRepo, refreshStore, testIssuer, slog.Default())

	sess, err := domain.NewSession("usr-sa", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-sa"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-sa", "usr-sa")
	require.NoError(t, err)

	// Normal refresh should succeed.
	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)

	// Revoke the session externally.
	sess, err = sessionRepo.GetByID(context.Background(), "sess-sa")
	require.NoError(t, err)
	sess.Revoke()
	require.NoError(t, sessionRepo.Update(context.Background(), sess))

	// Attempt refresh with the new (rotated) wire token — should be rejected
	// because the session is revoked.
	_, err = svc.Refresh(context.Background(), pair.RefreshToken)
	assert.Error(t, err, "revoked session must reject even a fresh wire token")
}

// TestRefresh_FailClosedWhenUserUnavailable verifies the F1 fail-closed policy:
// when userRepo.GetByID returns an error (user deleted mid-session), refresh
// must return ErrAuthRefreshFailed rather than signing a new access token.
func TestRefresh_FailClosedWhenUserUnavailable(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository() // intentionally empty — GetByID returns error
	refreshStore := newTestRefreshStore()
	svc := NewService(sessionRepo, roleRepo, userRepo, refreshStore, testIssuer, slog.Default())

	sess, err := domain.NewSession("usr-missing", "at", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-missing"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-missing", "usr-missing")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "fail-closed: refresh must error when user is unavailable")
	assert.Nil(t, pair)
}

// TestRefresh_FlagPropagatesFromCurrentUser_AfterClear ensures that after a
// user clears PasswordResetRequired, the next refresh produces a new access
// token with password_reset_required=false.
func TestRefresh_FlagPropagatesFromCurrentUser_AfterClear(t *testing.T) {
	svc, sessionRepo, userRepo := newTestServiceWithUserRepo()

	// Seed a user with reset flag = false (already cleared).
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("ref-user-clear", "ref-clear@test.com", string(hash))
	user.ID = "usr-ref-clear"
	// PasswordResetRequired is false by default.
	require.NoError(t, userRepo.Create(context.Background(), user))

	// We need the refreshStore from the service — recreate with known refreshStore.
	refreshStore := newTestRefreshStore()
	svc2 := NewService(sessionRepo, mem.NewRoleRepository(), userRepo, refreshStore, testIssuer, slog.Default())
	_ = svc // suppress unused var

	sess, _ := domain.NewSession("usr-ref-clear", "at", time.Now().Add(time.Hour))
	sess.ID = "sess-ref-clear"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-ref-clear", "usr-ref-clear")
	require.NoError(t, err)

	pair, err := svc2.Refresh(context.Background(), wireToken)
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
	sessionRepo := mem.NewSessionRepository()
	userRepo := mem.NewUserRepository()

	// Seed a user with reset flag = true.
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("ref-user-reset", "ref-reset@test.com", string(hash))
	user.ID = "usr-ref-reset"
	user.MarkPasswordResetRequired()
	require.NoError(t, userRepo.Create(context.Background(), user))

	refreshStore := newTestRefreshStore()
	svc := NewService(sessionRepo, mem.NewRoleRepository(), userRepo, refreshStore, testIssuer, slog.Default())

	sess, _ := domain.NewSession("usr-ref-reset", "at", time.Now().Add(time.Hour))
	sess.ID = "sess-ref-reset"
	require.NoError(t, sessionRepo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-ref-reset", "usr-ref-reset")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	assert.True(t, pair.PasswordResetRequired, "refreshed token must still have claim=true when user hasn't changed password")

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, claims.PasswordResetRequired, "access token claim must be true when flag not cleared")
}

// TestService_Refresh_InfraErrorOnSessionLookup verifies that an infra error
// from sessionRepo.GetByID causes Refresh to fail closed.
func TestService_Refresh_InfraErrorOnSessionLookup(t *testing.T) {
	infraErr := fmt.Errorf("db connection timeout")
	sessionRepo := &infraGetByIDRepo{
		SessionRepository: *mem.NewSessionRepository(),
		infraErr:          infraErr,
	}
	roleRepo := mem.NewRoleRepository()
	userRepo := mem.NewUserRepository()

	refreshStore := newTestRefreshStore()
	svc := NewService(sessionRepo, roleRepo, userRepo, refreshStore, testIssuer, slog.Default())

	// Issue a wire token but don't seed the session — GetByID will return infraErr.
	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-infra", "usr-infra")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "infra error must cause Refresh to fail")
	assert.Nil(t, pair)
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED")
}

// infraGetByIDRepo overrides GetByID to return an infra error.
type infraGetByIDRepo struct {
	mem.SessionRepository
	infraErr error
}

func (r *infraGetByIDRepo) GetByID(_ context.Context, _ string) (*domain.Session, error) {
	return nil, r.infraErr
}
