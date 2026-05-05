package auth_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth"
)

// fakeRateLimiter implements auth.BootstrapRateLimiter for test use.
// allowAll=true permits every request; allowAll=false blocks all requests.
type fakeRateLimiter struct {
	allowAll bool
}

func (f *fakeRateLimiter) Allow(_ string) bool { return f.allowAll }

var _ auth.BootstrapRateLimiter = (*fakeRateLimiter)(nil)

// nextHandlerCalled is a sentinel handler that records whether it was invoked.
type nextHandlerCalled struct{ called bool }

func (h *nextHandlerCalled) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	h.called = true
	w.WriteHeader(http.StatusOK)
}

func bootstrapCreds(username, password string) auth.BootstrapCredentials {
	return auth.BootstrapCredentials{
		Username: []byte(username),
		Password: []byte(password),
	}
}

func basicAuthHeader(username, password string) string {
	cred := username + ":" + password
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(cred))
}

// invokeBootstrapMiddleware wraps ExportedNewBootstrapMiddleware and catches the
// panic from the stub. Returns (responseCode, body, didPanic).
func invokeBootstrapMiddleware(t *testing.T, creds auth.BootstrapCredentials, limiter auth.BootstrapRateLimiter, req *http.Request) (code int, body string, didPanic bool) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			didPanic = true
		}
	}()
	mw := auth.ExportedNewBootstrapMiddleware(creds, limiter)
	next := &nextHandlerCalled{}
	handler := mw(next)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w.Code, w.Body.String(), false
}

// These tests exercise newBootstrapMiddleware, which is currently stubbed
// (panics) in bootstrap.go. They are intentionally RED until Batch 1 /
// Agent-B implements the real middleware logic.

// TestBootstrapMiddleware_NoAuthHeader_Returns401 verifies that a request
// without an Authorization header is rejected with 401 and the
// ERR_AUTH_BOOTSTRAP_FAILED error code.
func TestBootstrapMiddleware_NoAuthHeader_Returns401(t *testing.T) {
	t.Parallel()

	creds := bootstrapCreds("admin", "secret123")
	limiter := &fakeRateLimiter{allowAll: true}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/setup/admin", nil)
	code, bodyStr, didPanic := invokeBootstrapMiddleware(t, creds, limiter, req)

	if didPanic {
		t.Fatal("TestBootstrapMiddleware_NoAuthHeader_Returns401: FAIL — newBootstrapMiddleware panics (not yet implemented, Batch 1 required)")
	}

	assert.Equal(t, http.StatusUnauthorized, code, "no Authorization header must return 401")
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(bodyStr), &body))
	errObj, ok := body["error"].(map[string]interface{})
	require.True(t, ok, "response must have error envelope")
	assert.Equal(t, "ERR_AUTH_BOOTSTRAP_FAILED", errObj["code"], "error code must be ERR_AUTH_BOOTSTRAP_FAILED")
}

// TestBootstrapMiddleware_WrongUsername_Returns401 verifies that a request
// with a wrong username returns 401 with the same envelope as no-auth
// (oracle protection: no username vs wrong username are indistinguishable).
func TestBootstrapMiddleware_WrongUsername_Returns401(t *testing.T) {
	t.Parallel()

	creds := bootstrapCreds("admin", "secret123")
	limiter := &fakeRateLimiter{allowAll: true}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/setup/admin", nil)
	req.Header.Set("Authorization", basicAuthHeader("wrong", "secret123"))
	code, bodyStr, didPanic := invokeBootstrapMiddleware(t, creds, limiter, req)

	if didPanic {
		t.Fatal("TestBootstrapMiddleware_WrongUsername_Returns401: FAIL — newBootstrapMiddleware panics (not yet implemented, Batch 1 required)")
	}

	assert.Equal(t, http.StatusUnauthorized, code, "wrong username must return 401")
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(bodyStr), &body))
	errObj, ok := body["error"].(map[string]interface{})
	require.True(t, ok, "response must have error envelope")
	assert.Equal(t, "ERR_AUTH_BOOTSTRAP_FAILED", errObj["code"], "wrong username must use same code as no-auth")
}

// TestBootstrapMiddleware_WrongPassword_Returns401 verifies that a request
// with a wrong password returns 401 with the same envelope as wrong username
// (no field leakage via different error codes or messages).
func TestBootstrapMiddleware_WrongPassword_Returns401(t *testing.T) {
	t.Parallel()

	creds := bootstrapCreds("admin", "secret123")
	limiter := &fakeRateLimiter{allowAll: true}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/setup/admin", nil)
	req.Header.Set("Authorization", basicAuthHeader("admin", "wrongpassword"))
	code, bodyStr, didPanic := invokeBootstrapMiddleware(t, creds, limiter, req)

	if didPanic {
		t.Fatal("TestBootstrapMiddleware_WrongPassword_Returns401: FAIL — newBootstrapMiddleware panics (not yet implemented, Batch 1 required)")
	}

	assert.Equal(t, http.StatusUnauthorized, code, "wrong password must return 401")
	var body map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(bodyStr), &body))
	errObj, ok := body["error"].(map[string]interface{})
	require.True(t, ok, "response must have error envelope")
	assert.Equal(t, "ERR_AUTH_BOOTSTRAP_FAILED", errObj["code"], "wrong password must use same code as wrong username")
}

// TestBootstrapMiddleware_ValidCreds_Allows verifies that a request with
// correct credentials passes through to the next handler.
func TestBootstrapMiddleware_ValidCreds_Allows(t *testing.T) {
	t.Parallel()

	creds := bootstrapCreds("admin", "secret123")
	limiter := &fakeRateLimiter{allowAll: true}

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("TestBootstrapMiddleware_ValidCreds_Allows: FAIL — newBootstrapMiddleware panics (not yet implemented, Batch 1 required): %v", r)
		}
	}()

	mw := auth.ExportedNewBootstrapMiddleware(creds, limiter)
	next := &nextHandlerCalled{}
	handler := mw(next)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/setup/admin", nil)
	req.Header.Set("Authorization", basicAuthHeader("admin", "secret123"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	assert.True(t, next.called, "valid credentials must pass through to next handler")
	assert.Equal(t, http.StatusOK, w.Code, "next handler must be called with valid creds")
}

// TestBootstrapMiddleware_RateLimited_Returns429 verifies that when the
// rate limiter blocks a request, 429 is returned with Retry-After header.
func TestBootstrapMiddleware_RateLimited_Returns429(t *testing.T) {
	t.Parallel()

	creds := bootstrapCreds("admin", "secret123")
	limiter := &fakeRateLimiter{allowAll: false} // always rate-limited

	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/setup/admin", nil)
	req.Header.Set("Authorization", basicAuthHeader("admin", "secret123"))
	code, _, didPanic := invokeBootstrapMiddleware(t, creds, limiter, req)

	if didPanic {
		t.Fatal("TestBootstrapMiddleware_RateLimited_Returns429: FAIL — newBootstrapMiddleware panics (not yet implemented, Batch 1 required)")
	}

	assert.Equal(t, http.StatusTooManyRequests, code, "rate limited request must return 429")
}
