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

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionvalidate"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// loginAndGetPair seeds a user (via cell's seed-admin option), calls the real
// login HTTP handler through the initialized router, and returns the issued
// token pair on a precise 201 response. Failing this helper means the public
// login handler's status code or envelope drifted from the contract.
func loginAndGetPair(t *testing.T) (accessToken, refreshToken string, r *router.Router, c *AccessCore) {
	t.Helper()

	c = NewAccessCore(
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(noopPublisher{}),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		// Demo mode: no tx+outbox required.
		WithSeedAdmin("alice", testPassword),
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
