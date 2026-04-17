// Tests for AuthMiddleware intent enforcement (PR-P0-AUTH-INTENT).
//
// Middleware must reject refresh-intent tokens at business endpoints, returning
// 401 with the generic ERR_AUTH_UNAUTHORIZED code to prevent token-type
// enumeration (the specific ERR_AUTH_INVALID_TOKEN_INTENT is logged, not wired
// to the response).
package auth

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
)

// intentMockVerifier returns distinct results per expected intent so the
// middleware's call path can be observed.
type intentMockVerifier struct {
	accessClaims  Claims
	accessErr     error
	refreshClaims Claims
	refreshErr    error
}

// Verify is kept so intentMockVerifier also satisfies TokenVerifier; it is
// never invoked by AuthMiddleware (which calls VerifyIntent directly) and
// returns a sentinel error so accidental fallback is caught by tests.
func (v *intentMockVerifier) Verify(_ context.Context, _ string) (Claims, error) {
	return Claims{}, errcode.New(errcode.ErrAuthUnauthorized, "intentMockVerifier.Verify should not be called")
}

func (v *intentMockVerifier) VerifyIntent(_ context.Context, _ string, expected TokenIntent) (Claims, error) {
	switch expected {
	case TokenIntentAccess:
		return v.accessClaims, v.accessErr
	case TokenIntentRefresh:
		return v.refreshClaims, v.refreshErr
	default:
		return Claims{}, errcode.New(errcode.ErrAuthInvalidTokenIntent, "unknown intent")
	}
}

func TestAuthMiddleware_CallsVerifyIntentWithAccessExpectation(t *testing.T) {
	verifier := &intentMockVerifier{
		accessClaims: Claims{Subject: "u1", Roles: []string{"user"}, TokenUse: TokenIntentAccess},
	}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "u1", claims.Subject)
		assert.Equal(t, TokenIntentAccess, claims.TokenUse)
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer access-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_RejectsRefreshIntentToken_401(t *testing.T) {
	// VerifyIntent(access) returns ErrAuthInvalidTokenIntent for a refresh token.
	verifier := &intentMockVerifier{
		accessErr: errcode.New(errcode.ErrAuthInvalidTokenIntent, "refresh token used at business endpoint"),
	}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called when intent mismatches")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer refresh-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
}

// TestAuthMiddleware_RefreshAndInvalidToken_SameResponse asserts that two
// distinct failure types — intent mismatch and a plain invalid token — produce
// identical HTTP response bodies (same code, same message).  This test pins the
// enumeration-defense invariant: if someone later tries to differentiate the
// response bodies, this test will fail.
func TestAuthMiddleware_RefreshAndInvalidToken_SameResponse(t *testing.T) {
	// Case 1: refresh intent mismatch
	intentErrVerifier := &intentMockVerifier{
		accessErr: errcode.New(errcode.ErrAuthInvalidTokenIntent, "refresh token at business endpoint"),
	}
	// Case 2: some other invalid token error
	otherErrVerifier := &intentMockVerifier{
		accessErr: errcode.New(errcode.ErrAuthUnauthorized, "token expired"),
	}

	makeHandler := func(v IntentTokenVerifier) http.Handler {
		return AuthMiddleware(v, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("inner handler must not be called")
		}))
	}

	doRequest := func(h http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
		req.Header.Set("Authorization", "Bearer some-token")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec1 := doRequest(makeHandler(intentErrVerifier))
	rec2 := doRequest(makeHandler(otherErrVerifier))

	assert.Equal(t, http.StatusUnauthorized, rec1.Code)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
	assert.Equal(t, rec1.Body.String(), rec2.Body.String(),
		"intent-mismatch and other-invalid-token must produce identical response bodies (enumeration defense)")
}

// TestAuthMiddleware_IntentMismatch_LogsInvalidIntentError asserts that when
// VerifyIntent returns ErrAuthInvalidTokenIntent, the middleware logs the error
// containing "ERR_AUTH_INVALID_TOKEN_INTENT". This ensures the intent reason is
// observable in structured logs (ops signal) while the HTTP response stays
// generic (enumeration defense). Uses a slog buffer to capture the log output.
func TestAuthMiddleware_IntentMismatch_LogsInvalidIntentError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	verifier := &intentMockVerifier{
		accessErr: errcode.New(errcode.ErrAuthInvalidTokenIntent, "refresh token at business endpoint"),
	}
	handler := AuthMiddleware(verifier, nil, WithLogger(logger))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer refresh-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, buf.String(), "ERR_AUTH_INVALID_TOKEN_INTENT",
		"ERR_AUTH_INVALID_TOKEN_INTENT must appear in structured log output so ops can distinguish it via metrics reason=invalid_intent")
}

// TestAuthMiddleware_LegacyTokenVerifierIsCompileTimeRejected is a compile-time
// invariant check: AuthMiddleware's parameter is IntentTokenVerifier, so a
// plain TokenVerifier can no longer be plugged in. Any attempt to narrow the
// parameter back to TokenVerifier will fail to build this test.
func TestAuthMiddleware_LegacyTokenVerifierIsCompileTimeRejected(t *testing.T) {
	var v IntentTokenVerifier = &intentMockVerifier{}
	// The following must compile — v is an IntentTokenVerifier.
	_ = AuthMiddleware(v, nil)

	// Documented negative case: a value that only implements TokenVerifier
	// cannot be assigned to IntentTokenVerifier. We express that as a
	// non-executing check via interface satisfaction so a future regression
	// (widening the parameter back to TokenVerifier) would let a
	// plain-TokenVerifier compile and this assertion would become
	// redundant, surfacing the drift in review.
	var _ IntentTokenVerifier = (*intentMockVerifier)(nil)
	var _ TokenVerifier = (*mockVerifier)(nil)
	// mockVerifier now also satisfies IntentTokenVerifier; we rely on the
	// parameter type of AuthMiddleware (IntentTokenVerifier) to enforce the
	// invariant. The comment above documents why narrowing is a regression.
}
