package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockVerifier implements TokenVerifier for testing.
type mockVerifier struct {
	claims Claims
	err    error
}

func (v *mockVerifier) Verify(_ context.Context, _ string) (Claims, error) {
	return v.claims, v.err
}

// mockAuthorizer implements Authorizer for testing.
type mockAuthorizer struct {
	allowed bool
	err     error
}

func (a *mockAuthorizer) Authorize(_ context.Context, _, _, _ string) (bool, error) {
	return a.allowed, a.err
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "user-1", Roles: []string{"admin"}},
	}
	handler := AuthMiddleware(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "user-1", claims.Subject)

		sub, ok := ctxkeys.SubjectFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "user-1", sub)

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	verifier := &mockVerifier{}
	handler := AuthMiddleware(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	verifier := &mockVerifier{err: errors.New("expired")}
	handler := AuthMiddleware(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_NonBearerScheme(t *testing.T) {
	verifier := &mockVerifier{}
	handler := AuthMiddleware(verifier)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireRole_HasRole(t *testing.T) {
	handler := RequireRole(nil, "admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithClaims(req.Context(), Claims{Subject: "u1", Roles: []string{"admin", "user"}})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireRole_MissingRole(t *testing.T) {
	handler := RequireRole(nil, "admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithClaims(req.Context(), Claims{Subject: "u1", Roles: []string{"user"}})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assertErrorCode(t, rec, "ERR_AUTH_FORBIDDEN")
}

func TestRequireRole_NoClaims(t *testing.T) {
	handler := RequireRole(nil, "admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireRole_AuthorizerFallback(t *testing.T) {
	authorizer := &mockAuthorizer{allowed: true}
	handler := RequireRole(authorizer, "editor")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/docs", nil)
	ctx := WithClaims(req.Context(), Claims{Subject: "u1", Roles: []string{"viewer"}})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRequireRole_AuthorizerError(t *testing.T) {
	authorizer := &mockAuthorizer{err: errors.New("policy engine down")}
	handler := RequireRole(authorizer, "admin")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithClaims(req.Context(), Claims{Subject: "u1", Roles: []string{"user"}})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, code, errObj["code"])
}
