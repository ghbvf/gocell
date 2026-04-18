// AUTH-INT-REACHABILITY-01 + PR-P0-AUTH-INTENT cell-level integration tests.
//
// These tests exercise the end-to-end composition of session-login (to mint a
// real access/refresh pair), session-validate (access-only verifier used by
// middleware), and session-refresh (refresh-only verifier) so that:
//
//  1. a legitimate access token actually reaches the verifier's "allow" path
//     (reachability — previously only negative-path coverage existed),
//  2. the public login handler returns a precisely-shaped 201 response, and
//  3. cross-intent replay is rejected by the appropriate verifier:
//     refresh→business=reject, access→refresh=reject.
package accesscore

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/slices/rbacassign"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogout"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionvalidate"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
)

// loginAndGetPair pre-fills a user directly into repos, calls the real
// login HTTP handler through the initialized router, and returns the issued
// token pair on a precise 201 response. Failing this helper means the public
// login handler's status code or envelope drifted from the contract.
func loginAndGetPair(t *testing.T) (accessToken, refreshToken string, r *router.Router, c *AccessCore) {
	t.Helper()

	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	ctx := context.Background()

	// Pre-fill alice as admin via direct repo seeding (no bootstrap flow).
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), domain.BcryptCost)
	require.NoError(t, err)
	alice, err := domain.NewUser("alice", "alice@gocell.local", string(hash))
	require.NoError(t, err)
	alice.ID = "usr-alice-integration"
	require.NoError(t, roleRepo.Create(ctx, &domain.Role{
		ID: domain.RoleAdmin, Name: domain.RoleAdmin,
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	}))
	require.NoError(t, userRepo.Create(ctx, alice))
	_, err = roleRepo.AssignToUser(ctx, alice.ID, domain.RoleAdmin)
	require.NoError(t, err)

	c = NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(roleRepo),
		WithPublisher(noopPublisher{}),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		// Demo mode: no tx+outbox required.
	)
	require.NoError(t, c.Init(context.Background(), cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}))

	r = router.New()
	c.RegisterRoutes(r)

	body := strings.NewReader(`{"username":"alice","password":"` + testPassword + `"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code,
		"AUTH-INT-REACHABILITY-01: public login handler must return precisely 201, got %d body=%s",
		rec.Code, rec.Body.String())

	var envelope struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.AccessToken, "login response must include accessToken")
	require.NotEmpty(t, envelope.Data.RefreshToken, "login response must include refreshToken")
	require.NotEmpty(t, envelope.Data.ExpiresAt, "login response must include expiresAt")
	return envelope.Data.AccessToken, envelope.Data.RefreshToken, r, c
}

func TestAuthIntent_AccessTokenReachesBusinessPath(t *testing.T) {
	accessToken, _, _, c := loginAndGetPair(t)

	// session-validate is wired with the JWT verifier inside cell.go. We call
	// it directly to mirror how AuthMiddleware would consult it.
	validateSvc, ok := c.TokenVerifier().(*sessionvalidate.Service)
	require.True(t, ok, "TokenVerifier must be *sessionvalidate.Service in production wiring")

	claims, err := validateSvc.Verify(context.Background(), accessToken)
	require.NoError(t, err, "legitimate access token must pass session-validate")
	assert.NotEmpty(t, claims.Subject)
}

func TestAuthIntent_RefreshTokenBlockedAtBusinessPath(t *testing.T) {
	_, refreshToken, _, c := loginAndGetPair(t)

	validateSvc, ok := c.TokenVerifier().(*sessionvalidate.Service)
	require.True(t, ok)

	_, err := validateSvc.Verify(context.Background(), refreshToken)
	require.Error(t, err,
		"refresh token must NOT be accepted by session-validate (token confusion defense)")
}

func TestAuthIntent_AccessTokenBlockedAtRefreshPath(t *testing.T) {
	accessToken, _, _, c := loginAndGetPair(t)

	// Build a refresh-service that mirrors production wiring (jwtVerifier,
	// not validateSvc) so intent enforcement flows through.
	refreshSvc := sessionrefresh.NewService(
		c.sessionRepo, c.roleRepo, c.jwtIssuer, c.jwtVerifier, slog.Default(),
	)

	_, err := refreshSvc.Refresh(context.Background(), accessToken)
	require.Error(t, err,
		"access token must NOT be accepted by session-refresh (token confusion defense)")
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED",
		"intent mismatch collapses into ERR_AUTH_REFRESH_FAILED (enumeration defense)")
}

func TestAuthIntent_RefreshTokenSucceedsAtRefreshPath(t *testing.T) {
	_, refreshToken, _, c := loginAndGetPair(t)

	// Need sessionlogin's persisted session (loginAndGetPair went through the
	// real login flow, so c.sessionRepo already has one).
	require.NotNil(t, c.sessionRepo, "session repo must be wired")

	refreshSvc := sessionrefresh.NewService(
		c.sessionRepo, c.roleRepo, c.jwtIssuer, c.jwtVerifier, slog.Default(),
	)

	newPair, err := refreshSvc.Refresh(context.Background(), refreshToken)
	require.NoError(t, err, "legitimate refresh token must rotate successfully")
	assert.NotEmpty(t, newPair.AccessToken)
	assert.NotEmpty(t, newPair.RefreshToken)
	// NB: can't strictly require newPair.RefreshToken != refreshToken because
	// iat/exp are second-granular; a same-second rotation hashes to the same
	// signed JWT. Domain-level uniqueness is tracked by the sessionRepo
	// rotation test in sessionrefresh, not here.
	accessClaims, err := testVerifier.VerifyIntent(context.Background(), newPair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err, "rotated access token must carry intent=access")
	assert.NotEmpty(t, accessClaims.Subject)
}

// TestAuthIntegration_RoleRevokeInvalidatesSession exercises the full transactional
// outbox path at the service layer:
//
//  1. Seed admin role + two users (bob with session, admin with a second admin role holder).
//  2. Revoke bob's "member" role via rbacassign.Service in durable mode
//     (stubOutboxWriter + stubTxRunner injected via WithOutboxWriter / WithTxManager).
//  3. Deliver the outbox entry synchronously to the sessionlogout consumer.
//  4. Assert that bob's session is now revoked.
//
// This is a slice-layer integration test (not HTTP) because the HTTP round-trip adds
// noise without testing the outbox→consumer wiring. The test runs the full service
// composition to mirror cell.Init wiring without the HTTP router overhead.
//
// NOTE: The EventRouter / ConsumerBase dispatch path is tested by the kernel/outbox
// and runtime/eventbus packages. Here we test the application-layer contract: that
// rbacassign produces the right outbox entry and the consumer handles it correctly.
func TestAuthIntegration_RoleRevokeInvalidatesSession(t *testing.T) {
	ctx := context.Background()

	// Shared repos (simulates cell's single repo wiring).
	roleRepo := mem.NewRoleRepository()
	sessionRepo := mem.NewSessionRepository()

	// Seed "member" role.
	roleRepo.SeedRole(&domain.Role{ID: "member", Name: "member"})
	// Seed "admin" role so bob doesn't become the last admin.
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	// Assign bob and carol to "member" so last-holder guard doesn't block.
	_, _ = roleRepo.AssignToUser(ctx, "usr-bob", "member")
	_, _ = roleRepo.AssignToUser(ctx, "usr-carol", "member")

	// Give bob an active session.
	bobSession := &domain.Session{ID: "sess-bob", UserID: "usr-bob"}
	require.NoError(t, sessionRepo.Create(ctx, bobSession))

	// Wire rbacassign with outbox stubs (durable mode).
	stubWriter := &rbacStubOutboxWriter{}
	stubTx := &rbacStubTxRunner{}
	assignSvc := rbacassign.NewService(roleRepo, sessionRepo, slog.Default(),
		rbacassign.WithOutboxWriter(stubWriter),
		rbacassign.WithTxManager(stubTx),
	)

	// Wire the sessionlogout consumer.
	consumer := sessionlogout.NewConsumer(sessionRepo, slog.Default())

	// Revoke bob's member role — should produce one outbox entry.
	require.NoError(t, assignSvc.Revoke(ctx, "usr-bob", "member"))
	require.Len(t, stubWriter.entries, 1, "Revoke must produce exactly one outbox entry")

	// Deliver the outbox entry synchronously to the consumer (simulates relay dispatch).
	require.NoError(t, consumer.HandleRoleChanged(ctx, stubWriter.entries[0]))

	// Bob's session must now be revoked.
	sess, err := sessionRepo.GetByID(ctx, "sess-bob")
	require.NoError(t, err)
	assert.True(t, sess.IsRevoked(),
		"session must be revoked after role-revoke outbox entry is consumed")
}

// rbacStubOutboxWriter captures entries for slice-layer integration tests.
// Defined here to avoid package-crossing (rbacassign is a different package).
type rbacStubOutboxWriter struct {
	entries []outbox.Entry
}

func (w *rbacStubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

// rbacStubTxRunner executes fn directly (no real transaction), simulating in-memory behaviour.
type rbacStubTxRunner struct{}

func (rbacStubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// noopPublisher implements eventbus.Publisher for tests that do not care
// about published events. Keeps AccessCore.Init happy in demo mode.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

// Compile-time proof these tests hit the real slices (not stubs).
var (
	_ = (*sessionlogin.Service)(nil)
	_ = (*sessionrefresh.Service)(nil)
	_ = (*sessionvalidate.Service)(nil)
)
