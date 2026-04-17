package auth

// middleware_aud_test.go: HTTP route-level audience regression tests.
// These tests exercise the full AuthMiddleware → JWTVerifier chain via
// httptest.ResponseRecorder, ensuring that wrong-audience and missing-audience
// bearer tokens are rejected BEFORE reaching any handler — not just at the
// VerifyIntent helper level.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildAudTestPair returns an issuer and verifier pair for audience tests.
// The verifier expects audience "gocell"; the issuer can produce any audience.
func buildAudTestPair(t *testing.T) (*JWTIssuer, *JWTVerifier) {
	t.Helper()
	ks := mustTestKeySet(t)
	issuer, err := NewJWTIssuer(ks, "test", 15*time.Minute)
	require.NoError(t, err)
	verifier, err := NewJWTVerifier(ks, WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	return issuer, verifier
}

// audProtectedHandler is a trivial handler that writes 200 when reached.
var audProtectedHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
})

func TestAuthMiddleware_WrongAudience_Returns401(t *testing.T) {
	issuer, verifier := buildAudTestPair(t)
	// Issue token for "other-service", not "gocell"
	token, err := issuer.Issue(TokenIntentAccess, "alice", nil, []string{"other-service"}, "")
	require.NoError(t, err)

	h := AuthMiddleware(verifier, nil)(audProtectedHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"wrong-audience access token must be rejected by AuthMiddleware before reaching handler")
}

func TestAuthMiddleware_MissingAudience_Returns401(t *testing.T) {
	issuer, verifier := buildAudTestPair(t)
	// Issue token with no audience at all
	token, err := issuer.Issue(TokenIntentAccess, "alice", nil, nil, "")
	require.NoError(t, err)

	h := AuthMiddleware(verifier, nil)(audProtectedHandler)

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"missing-audience access token must be rejected by AuthMiddleware before reaching handler")
}

func TestAuthMiddleware_WrongAudience_RefreshPath_Returns401(t *testing.T) {
	issuer, verifier := buildAudTestPair(t)
	// Issue a refresh token (right intent) but wrong audience
	token, err := issuer.Issue(TokenIntentRefresh, "alice", nil, []string{"other-service"}, "")
	require.NoError(t, err)

	// Simulate a refresh endpoint: uses VerifyIntent(refresh) directly.
	// This tests that audience enforcement propagates into VerifyIntent when
	// called by application code (not just by AuthMiddleware).
	refreshHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bearer := r.Header.Get("Authorization")
		if len(bearer) > 7 {
			bearer = bearer[7:]
		}
		_, err := verifier.VerifyIntent(r.Context(), bearer, TokenIntentRefresh)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/auth/refresh", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	refreshHandler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"wrong-audience refresh token must be rejected before reaching refresh handler")
}
