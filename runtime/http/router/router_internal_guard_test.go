package router

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
