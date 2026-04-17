package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockVerifier implements IntentTokenVerifier for testing. Intent-specific
// behaviour is tested in middleware_intent_test.go.
type mockVerifier struct {
	claims Claims
	err    error
}

func (v *mockVerifier) VerifyIntent(_ context.Context, _ string, _ TokenIntent) (Claims, error) {
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
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "user-1", claims.Subject)

		sub, ok := ctxkeys.SubjectFrom(r.Context())
		assert.True(t, ok)
		assert.Equal(t, "user-1", sub)

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	verifier := &mockVerifier{}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
}

func TestAuthMiddleware_InvalidToken(t *testing.T) {
	verifier := &mockVerifier{err: errors.New("expired")}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_NonBearerScheme(t *testing.T) {
	verifier := &mockVerifier{}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_NilPublicEndpoints_AllPathsRequireAuth(t *testing.T) {
	// DefaultPublicEndpoints is intentionally empty. Passing nil means no
	// paths are public — the composition root must declare public endpoints
	// explicitly. This is the fail-closed default.
	verifier := &mockVerifier{err: errors.New("should not be called")}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// All paths should require auth when publicEndpoints is nil (empty default).
	for _, p := range []string{
		"/api/v1/access/sessions/login",
		"/api/v1/access/sessions/refresh",
		"/api/v1/data",
	} {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code,
				"nil publicEndpoints must not exempt any path from auth")
		})
	}
}

func TestAuthMiddleware_PublicEndpointCustomWhitelist(t *testing.T) {
	verifier := &mockVerifier{err: errors.New("should not be called")}
	handler := AuthMiddleware(verifier, []string{"/custom/public"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Custom public endpoint should pass without token.
	req := httptest.NewRequest(http.MethodGet, "/custom/public", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Default public endpoint should NOT be whitelisted.
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestAuthMiddleware_EmptyPublicEndpoints_NoDefaults(t *testing.T) {
	// Passing an explicit empty slice disables default public endpoints.
	// Every path requires auth — nil and []string{} have different semantics.
	verifier := &mockVerifier{err: errors.New("should not be called")}
	handler := AuthMiddleware(verifier, []string{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Even the default login path should require auth when empty list is passed.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"empty publicEndpoints must not use defaults — all paths should require auth")
}

func TestAuthMiddleware_PathCleanNormalization(t *testing.T) {
	// Auth middleware uses path.Clean on both whitelist entries and incoming
	// request paths. This prevents bypasses via double slashes or dot segments.
	verifier := &mockVerifier{err: errors.New("should not be called")}
	handler := AuthMiddleware(verifier, []string{"/api/v1/login"})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name   string
		path   string
		expect int
	}{
		{"exact match", "/api/v1/login", http.StatusOK},
		{"double slash", "/api/v1//login", http.StatusOK},
		{"dot segment", "/api/v1/./login", http.StatusOK},
		{"non-matching", "/api/v1/data", http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.Equal(t, tc.expect, rec.Code)
		})
	}
}

func TestAuthMiddleware_ProtectedEndpointNoToken(t *testing.T) {
	verifier := &mockVerifier{}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/protected", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
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

func TestAuthMiddleware_WithLogger_LogsToBuffer(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	verifier := &mockVerifier{err: errors.New("token expired")}
	handler := AuthMiddleware(verifier, nil, WithLogger(logger))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not be called")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer bad-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, buf.String(), "token verification failed")
}

func TestAuthMiddleware_WithMetrics_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)

	verifier := &mockVerifier{claims: Claims{Subject: "user-1"}}
	handler := AuthMiddleware(verifier, nil, WithMetrics(am))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	// Success path.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req.Header.Set("Authorization", "Bearer valid-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Failure path.
	failVerifier := &mockVerifier{err: errors.New("expired")}
	failHandler := AuthMiddleware(failVerifier, nil, WithMetrics(am))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not be called")
		}),
	)
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/data", nil)
	req2.Header.Set("Authorization", "Bearer bad-token")
	rec2 := httptest.NewRecorder()
	failHandler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, code, errObj["code"])
	assert.Equal(t, map[string]any{}, errObj["details"], "canonical envelope must include empty details object")
}
