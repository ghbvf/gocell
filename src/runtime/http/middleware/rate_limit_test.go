package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLimiter implements RateLimiter for testing.
type mockLimiter struct {
	allowAll bool
	keys     []string
}

func (m *mockLimiter) Allow(key string) bool {
	m.keys = append(m.keys, key)
	return m.allowAll
}

func TestRateLimit_Allowed(t *testing.T) {
	limiter := &mockLimiter{allowAll: true}
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := ctxkeys.WithRealIP(req.Context(), "10.0.0.1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"10.0.0.1"}, limiter.keys)
}

func TestRateLimit_Rejected(t *testing.T) {
	limiter := &mockLimiter{allowAll: false}
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := ctxkeys.WithRealIP(req.Context(), "10.0.0.1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_RATE_LIMITED", errObj["code"])
}

func TestRateLimit_FallbackRemoteAddr(t *testing.T) {
	limiter := &mockLimiter{allowAll: true}
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// No RealIP in context; should fall back to RemoteAddr
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, limiter.keys, 1)
	assert.NotEmpty(t, limiter.keys[0])
}
