package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// windowedMockLimiter implements WindowedRateLimiter.
type windowedMockLimiter struct {
	allowAll bool
	window   time.Duration
	limit    int
	keys     []string
}

func (m *windowedMockLimiter) Allow(key string) bool {
	m.keys = append(m.keys, key)
	return m.allowAll
}

func (m *windowedMockLimiter) Window() (time.Duration, int) {
	return m.window, m.limit
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

func TestRateLimit_Rejected_DefaultRetryAfter(t *testing.T) {
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
	// Default Retry-After when limiter does not implement WindowedRateLimiter.
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_RATE_LIMITED", errObj["code"])
	assert.Equal(t, map[string]any{}, errObj["details"], "canonical envelope must include empty details object")
}

func TestRateLimit_Rejected_DynamicRetryAfter(t *testing.T) {
	limiter := &windowedMockLimiter{
		allowAll: false,
		window:   time.Minute,
		limit:    10,
	}
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := ctxkeys.WithRealIP(req.Context(), "10.0.0.1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	// ceil(60s / 10) = 6 seconds.
	assert.Equal(t, "6", rec.Header().Get("Retry-After"))
}

func TestRateLimit_Rejected_DynamicRetryAfter_CeilRounding(t *testing.T) {
	limiter := &windowedMockLimiter{
		allowAll: false,
		window:   10 * time.Second,
		limit:    3,
	}
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := ctxkeys.WithRealIP(req.Context(), "10.0.0.1")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	// ceil(10s / 3) = ceil(3.33) = 4 seconds.
	assert.Equal(t, "4", rec.Header().Get("Retry-After"))
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

func TestRateLimit_FallbackRemoteAddr_StripsPort(t *testing.T) {
	limiter := &mockLimiter{allowAll: true}
	handler := RateLimit(limiter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.168.1.100:54321"
	// No RealIP in context; falls back to RemoteAddr with port stripped.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, limiter.keys, 1)
	assert.Equal(t, "192.168.1.100", limiter.keys[0],
		"port must be stripped from RemoteAddr so same IP shares one bucket")
}

func TestComputeRetryAfter_NonWindowedLimiter(t *testing.T) {
	limiter := &mockLimiter{}
	assert.Equal(t, 1, computeRetryAfter(limiter))
}

func TestComputeRetryAfter_WindowedLimiter(t *testing.T) {
	tests := []struct {
		name   string
		window time.Duration
		limit  int
		want   int
	}{
		{"60s/10", time.Minute, 10, 6},
		{"10s/3 (ceil)", 10 * time.Second, 3, 4},
		{"1s/1", time.Second, 1, 1},
		{"30s/100", 30 * time.Second, 100, 1},
		{"zero limit fallback", time.Minute, 0, 1},
		{"zero window fallback", 0, 10, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := &windowedMockLimiter{window: tt.window, limit: tt.limit}
			assert.Equal(t, tt.want, computeRetryAfter(limiter))
		})
	}
}
