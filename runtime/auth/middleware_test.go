package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestAuthMiddleware_WithPublicEndpointMatcher_MethodAware(t *testing.T) {
	// WithPublicEndpointMatcher: only POST /foo is public; GET must require auth.
	verifier := &mockVerifier{err: errors.New("should not be called for POST")}

	matcher := func(r *http.Request) bool {
		return r.Method == "POST" && r.URL.Path == "/foo"
	}

	handler := AuthMiddleware(verifier, nil, WithPublicEndpointMatcher(matcher))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	// POST /foo → public, no token needed.
	req := httptest.NewRequest(http.MethodPost, "/foo", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code, "POST /foo must bypass auth via matcher")

	// GET /foo → not public, 401.
	req = httptest.NewRequest(http.MethodGet, "/foo", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"GET /foo must require auth when only POST is declared public via matcher")
}

func TestAuthMiddleware_WithPublicEndpointMatcher_OverridesSliceParam(t *testing.T) {
	// When WithPublicEndpointMatcher is set, the []string publicEndpoints param
	// is ignored for bypass decisions.
	verifier := &mockVerifier{err: errors.New("should not be called")}

	// Matcher allows nothing — even though the publicEndpoints param has /bar.
	matcher := func(_ *http.Request) bool { return false }

	handler := AuthMiddleware(verifier, []string{"/bar"}, WithPublicEndpointMatcher(matcher))(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	// /bar is in the []string list but matcher says no → must require auth.
	req := httptest.NewRequest(http.MethodGet, "/bar", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"matcher must take precedence over []string publicEndpoints parameter")
}

// TestAuthMiddleware_LegacyStringWithSpace_Panics tests I-2: defense-in-depth
// detection of callers that accidentally pass "METHOD /path" format to the
// legacy []string path.
func TestAuthMiddleware_LegacyStringWithSpace_Panics(t *testing.T) {
	verifier := &mockVerifier{}
	defer func() {
		r := recover()
		require.NotNil(t, r)
		require.Contains(t, fmt.Sprint(r), "METHOD /path")
	}()
	_ = AuthMiddleware(verifier, []string{"POST /foo"})
	t.Fatal("expected panic")
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, code string) {
	t.Helper()
	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, code, errObj["code"])
	// ERR_AUTH_PASSWORD_RESET_REQUIRED carries a change_password_endpoint hint
	// in details (P2-10 fix). All other codes must have an empty details object.
	if code != "ERR_AUTH_PASSWORD_RESET_REQUIRED" {
		assert.Equal(t, map[string]any{}, errObj["details"], "canonical envelope must include empty details object")
	}
}

// assertPasswordResetErrorWithHint asserts 403 ERR_AUTH_PASSWORD_RESET_REQUIRED
// response and verifies the change_password_endpoint hint is present (P2-10).
func assertPasswordResetErrorWithHint(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_PASSWORD_RESET_REQUIRED", errObj["code"])
	details, ok := errObj["details"].(map[string]any)
	require.True(t, ok, "details must be a map")
	assert.Equal(t, "POST /api/v1/access/users/{id}/password", details["change_password_endpoint"],
		"403 password-reset response must include change_password_endpoint hint (P2-10)")
}

// testExemptMatcher returns the canonical (method, path) matcher used by the
// test suite when exercising the password-reset gate. Mirrors the matcher that
// cmd/core-bundle + examples/sso-bff compose via
// bootstrap.WithPasswordResetExemptEndpoints.
func testExemptMatcher(t *testing.T) func(method, urlPath string) bool {
	t.Helper()
	m, err := CompilePasswordResetExempts([]string{
		"POST /api/v1/access/users/{id}/password",
		"DELETE /api/v1/access/sessions/{id}",
	})
	require.NoError(t, err)
	return m
}

// TestAuthMiddleware_PasswordResetRequired_DefaultMatcherIsFailClosed verifies
// the F6 default: when no WithPasswordResetExemptMatcher is wired, every
// authenticated request with password_reset_required=true returns 403. This
// prevents runtime/auth from silently routing around cell-specific endpoints
// via hard-coded paths.
func TestAuthMiddleware_PasswordResetRequired_DefaultMatcherIsFailClosed(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-bootstrap", PasswordResetRequired: true},
	}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("no route must be exempt when no matcher is wired")
	}))

	for _, mp := range [][2]string{
		{http.MethodPost, "/api/v1/access/users/usr-1/password"},
		{http.MethodDelete, "/api/v1/access/sessions/sess-1"},
	} {
		req := httptest.NewRequest(mp[0], mp[1], nil)
		req.Header.Set("Authorization", "Bearer reset-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code,
			"fail-closed: %s %s must 403 without matcher opt-in", mp[0], mp[1])
	}
}

// --- Phase 3.5: PasswordResetRequired middleware enforcement tests ---

// TestAuthMiddleware_PasswordResetRequired_BlocksBusinessRoute verifies that a
// token with PasswordResetRequired=true is blocked on non-exempt business
// routes. The composition root wires WithPasswordResetChangeEndpointHint so
// the 403 body carries the navigational hint; runtime/auth itself knows no
// business paths.
func TestAuthMiddleware_PasswordResetRequired_BlocksBusinessRoute(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-bootstrap", PasswordResetRequired: true},
	}
	handler := AuthMiddleware(verifier, nil,
		WithPasswordResetChangeEndpointHint("POST /api/v1/access/users/{id}/password"),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach business handler when password reset is required")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/configs", nil)
	req.Header.Set("Authorization", "Bearer reset-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assertPasswordResetErrorWithHint(t, rec)
}

// TestAuthMiddleware_PasswordResetRequired_AllowsChangePassword_PathTemplate verifies
// that POST /api/v1/access/users/{id}/password is exempt from the reset block
// when the composition root opts in via WithPasswordResetExemptMatcher.
func TestAuthMiddleware_PasswordResetRequired_AllowsChangePassword_PathTemplate(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-bootstrap-abc", PasswordResetRequired: true},
	}
	reached := false
	handler := AuthMiddleware(verifier, nil,
		WithPasswordResetExemptMatcher(testExemptMatcher(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/users/usr-bootstrap-abc/password", nil)
	req.Header.Set("Authorization", "Bearer reset-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, reached, "change-password endpoint must be reachable with password-reset token")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestAuthMiddleware_PasswordResetRequired_AllowsChangePassword_VariousIDs is a
// table-driven test that verifies the path template wildcard matches various ID formats.
func TestAuthMiddleware_PasswordResetRequired_AllowsChangePassword_VariousIDs(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr", PasswordResetRequired: true},
	}
	okHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
	}{
		{"uuid id", http.MethodPost, "/api/v1/access/users/550e8400-e29b-41d4-a716-446655440000/password", http.StatusOK},
		{"numeric id", http.MethodPost, "/api/v1/access/users/12345/password", http.StatusOK},
		{"kebab id", http.MethodPost, "/api/v1/access/users/usr-bootstrap-admin/password", http.StatusOK},
		{"id with dots", http.MethodPost, "/api/v1/access/users/usr.admin.1/password", http.StatusOK},
		{"path traversal with slash — no match", http.MethodPost, "/api/v1/access/users/usr/extra/password", http.StatusForbidden},
		{"wrong method GET — no match", http.MethodGet, "/api/v1/access/users/usr-1/password", http.StatusForbidden},
		{"no segment — no match", http.MethodPost, "/api/v1/access/users//password", http.StatusForbidden},
	}

	exempt := testExemptMatcher(t)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := AuthMiddleware(verifier, nil, WithPasswordResetExemptMatcher(exempt))(okHandler)
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Authorization", "Bearer reset-token")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantCode, rec.Code, "path=%s method=%s", tc.path, tc.method)
		})
	}
}

// TestAuthMiddleware_PasswordResetRequired_AllowsLogout verifies that
// DELETE /api/v1/access/sessions/{id} is exempt from the reset block when the
// composition root opts in via WithPasswordResetExemptMatcher.
func TestAuthMiddleware_PasswordResetRequired_AllowsLogout(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-bootstrap", PasswordResetRequired: true},
	}
	reached := false
	handler := AuthMiddleware(verifier, nil,
		WithPasswordResetExemptMatcher(testExemptMatcher(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/access/sessions/sess-xyz", nil)
	req.Header.Set("Authorization", "Bearer reset-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.True(t, reached, "logout endpoint must be reachable with password-reset token")
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestAuthMiddleware_PasswordResetRequired_BlocksWrongMethodOnExempt verifies
// that GET /api/v1/access/users/{id}/password is NOT exempt (only POST is).
func TestAuthMiddleware_PasswordResetRequired_BlocksWrongMethodOnExempt(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-bootstrap", PasswordResetRequired: true},
	}
	handler := AuthMiddleware(verifier, nil,
		WithPasswordResetChangeEndpointHint("POST /api/v1/access/users/{id}/password"),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("GET on change-password path must NOT be exempt")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/usr-1/password", nil)
	req.Header.Set("Authorization", "Bearer reset-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assertPasswordResetErrorWithHint(t, rec)
}

// TestAuthMiddleware_PasswordResetRequired_OmitsHintWhenNotConfigured verifies
// the runtime/auth default: without WithPasswordResetChangeEndpointHint, the
// 403 response body has NO details.change_password_endpoint — runtime/auth
// carries no business path knowledge, and the composition root opts in to
// any hint explicitly.
func TestAuthMiddleware_PasswordResetRequired_OmitsHintWhenNotConfigured(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-bootstrap", PasswordResetRequired: true},
	}
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not reach handler")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/configs", nil)
	req.Header.Set("Authorization", "Bearer reset-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_AUTH_PASSWORD_RESET_REQUIRED", errObj["code"])
	_, hasDetails := errObj["details"]
	assert.False(t, hasDetails,
		"without WithPasswordResetChangeEndpointHint, the response must carry no details map")
}

// TestAuthMiddleware_NoResetClaim_PassesThrough verifies that a regular token
// (PasswordResetRequired=false) is not affected by the new enforcement logic.
func TestAuthMiddleware_NoResetClaim_PassesThrough(t *testing.T) {
	verifier := &mockVerifier{
		claims: Claims{Subject: "usr-normal", Roles: []string{"user"}},
	}
	reached := false
	handler := AuthMiddleware(verifier, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	for _, path := range []string{"/api/v1/configs", "/api/v1/data", "/api/v1/access/users/me"} {
		t.Run(path, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer normal-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			assert.True(t, reached, "normal token must pass through to handler for path %s", path)
			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

// --- matchPathTemplate unit tests ---

func TestMatchPathTemplate(t *testing.T) {
	tests := []struct {
		template string
		concrete string
		want     bool
	}{
		{"/api/v1/access/users/{id}/password", "/api/v1/access/users/usr-abc/password", true},
		{"/api/v1/access/users/{id}/password", "/api/v1/access/users/12345/password", true},
		{"/api/v1/access/users/{id}/password", "/api/v1/access/users/u.1.2/password", true},
		{"/api/v1/access/users/{id}/password", "/api/v1/access/users//password", false}, // empty segment
		{"/api/v1/access/users/{id}/password", "/api/v1/access/users/usr/extra/password", false},
		{"/api/v1/access/users/{id}/password", "/api/v1/access/users/usr-abc/other", false},
		{"/api/v1/access/sessions/{id}", "/api/v1/access/sessions/sess-xyz", true},
		{"/api/v1/access/sessions/{id}", "/api/v1/access/sessions/", false}, // empty segment
		{"/static/path", "/static/path", true},
		{"/static/path", "/static/other", false},
	}

	for _, tc := range tests {
		t.Run(tc.template+"→"+tc.concrete, func(t *testing.T) {
			got := matchPathTemplate(tc.template, tc.concrete)
			assert.Equal(t, tc.want, got)
		})
	}
}
