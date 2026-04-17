// Tests for AuthMiddleware intent enforcement (PR-P0-AUTH-INTENT).
//
// Middleware must reject refresh-intent tokens at business endpoints, returning
// 401 with the generic ERR_AUTH_UNAUTHORIZED code to prevent token-type
// enumeration (the specific ERR_AUTH_INVALID_TOKEN_INTENT is logged, not wired
// to the response).
package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
)

// intentMockVerifier returns distinct results per expected intent so the
// middleware's call path can be observed.
type intentMockVerifier struct {
	accessClaims   Claims
	accessErr      error
	refreshClaims  Claims
	refreshErr     error
	fallbackClaims Claims
	fallbackErr    error
}

func (v *intentMockVerifier) Verify(_ context.Context, _ string) (Claims, error) {
	return v.fallbackClaims, v.fallbackErr
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
