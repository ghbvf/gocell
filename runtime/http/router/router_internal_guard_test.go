package router

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockIntentVerifier implements auth.IntentTokenVerifier for guard delegation tests.
type mockIntentVerifier struct {
	claims auth.Claims
	err    error
}

func (v *mockIntentVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return v.claims, v.err
}

// makeCountingGuard returns a middleware that increments counter on each
// request, then delegates to the wrapped handler.
func makeCountingGuard(counter *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

func TestWithInternalPathPrefixGuard_MatchesPrefix(t *testing.T) {
	// Requests matching the prefix must be wrapped by the guard.
	var counter atomic.Int64
	guard := makeCountingGuard(&counter)

	rtr, err := NewE(WithInternalPathPrefixGuard("/internal/v1/", guard))
	require.NoError(t, err)

	// Register a simple handler so the router can dispatch.
	rtr.mux.Get("/internal/v1/foo", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/foo", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)

	assert.Equal(t, int64(1), counter.Load(), "guard must be called for /internal/v1/* requests")
}

func TestWithInternalPathPrefixGuard_DoesNotMatchOther(t *testing.T) {
	// Requests NOT matching the prefix must NOT be wrapped by the guard.
	var counter atomic.Int64
	guard := makeCountingGuard(&counter)

	rtr, err := NewE(WithInternalPathPrefixGuard("/internal/v1/", guard))
	require.NoError(t, err)

	rtr.mux.Get("/api/v1/foo", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/foo", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)

	assert.Equal(t, int64(0), counter.Load(), "guard must NOT be called for non-matching paths")
}

func TestWithInternalPathPrefixGuard_EmptyPrefix_FailsFast(t *testing.T) {
	// Empty prefix must be rejected at NewE time.
	guard := makeCountingGuard(new(atomic.Int64))
	_, err := NewE(WithInternalPathPrefixGuard("", guard))
	require.Error(t, err, "empty prefix must cause NewE to fail")
	assert.Contains(t, err.Error(), "prefix")
}

func TestWithInternalPathPrefixGuard_PrefixMustStartWithSlash(t *testing.T) {
	// Prefix not starting with '/' must be rejected at NewE time.
	guard := makeCountingGuard(new(atomic.Int64))
	_, err := NewE(WithInternalPathPrefixGuard("internal/v1/", guard))
	require.Error(t, err, "prefix without leading slash must cause NewE to fail")
	assert.Contains(t, err.Error(), "prefix")
}

func TestWithInternalPathPrefixGuard_PrefixMustEndWithSlash(t *testing.T) {
	// Prefix not ending with '/' must be rejected at NewE time.
	guard := makeCountingGuard(new(atomic.Int64))
	_, err := NewE(WithInternalPathPrefixGuard("/internal/v1", guard))
	require.Error(t, err, "prefix without trailing slash must cause NewE to fail")
	assert.Contains(t, err.Error(), "prefix")
}

func TestWithInternalPathPrefixGuard_NilGuard_FailsFast(t *testing.T) {
	// Nil guard must be rejected at NewE time, symmetric with WithCircuitBreaker(nil).
	_, err := NewE(WithInternalPathPrefixGuard("/internal/v1/", nil))
	require.Error(t, err, "nil guard must cause NewE to fail")
	assert.Contains(t, err.Error(), "guard")
}

// TestWithInternalPathPrefixGuard_AutoDelegatesJWT verifies that installing the
// guard automatically marks its prefix as JWT-delegated. A request to
// /internal/v1/foo without an Authorization header must NOT receive a 401 from
// AuthMiddleware — instead it must reach the guard (which then decides auth).
func TestWithInternalPathPrefixGuard_AutoDelegatesJWT(t *testing.T) {
	// AuthMiddleware is configured with a verifier that always fails: if JWT
	// middleware runs for the internal path, it will 401.
	verifier := &mockIntentVerifier{err: errors.New("JWT must not run for delegated paths")}

	// Guard that records calls and writes a distinctive sentinel status.
	var guardCalled atomic.Int64
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guardCalled.Add(1)
			// Guard rejects without a service token (401 from guard, not from JWT).
			w.WriteHeader(http.StatusUnauthorized)
		})
	}

	rtr, err := NewE(
		WithAuthMiddleware(verifier, nil),
		WithInternalPathPrefixGuard("/internal/v1/", guard),
	)
	require.NoError(t, err)

	rtr.mux.Post("/internal/v1/access/roles/assign",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	// Request with no Authorization header — must NOT be 401'd by JWT middleware.
	// The guard must be invoked and own the 401 response.
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)

	// Guard must have been called — not JWT middleware.
	assert.Equal(t, int64(1), guardCalled.Load(),
		"guard must be invoked for /internal/v1/* when JWT is delegated")
	// The 401 here comes from the guard, not from AuthMiddleware — same status
	// code but different body. In prod the body would say "missing service token".
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"guard is the sole auth layer; it returns 401 when service token is absent")
}

// TestWithInternalPathPrefixGuard_JWTStillEnforcedForOtherPaths verifies that
// auto-delegation only applies to the guard prefix, not to /api/v1/* paths.
func TestWithInternalPathPrefixGuard_JWTStillEnforcedForOtherPaths(t *testing.T) {
	verifier := &mockIntentVerifier{err: errors.New("JWT enforced")}

	var guardCalled atomic.Int64
	guard := makeCountingGuard(&guardCalled)

	rtr, err := NewE(
		WithAuthMiddleware(verifier, nil),
		WithInternalPathPrefixGuard("/internal/v1/", guard),
	)
	require.NoError(t, err)

	rtr.mux.Get("/api/v1/configs",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))

	// /api/v1/* without Authorization → JWT middleware must reject.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/configs", nil)
	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, req)

	assert.Equal(t, int64(0), guardCalled.Load(), "guard must NOT be invoked for /api/v1/* paths")
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"/api/v1/* without JWT must still receive 401 from AuthMiddleware")
}
