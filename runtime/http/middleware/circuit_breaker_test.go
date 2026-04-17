package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBreaker implements Allower for testing.
type mockBreaker struct {
	open        bool
	doneErr     *error // captures the error passed to done()
	doneInvoked bool
}

func (m *mockBreaker) Allow() (bool, func(error)) {
	if m.open {
		return false, nil
	}
	return true, func(err error) {
		m.doneInvoked = true
		m.doneErr = &err
	}
}

func TestCircuitBreaker_Closed_PassesThrough(t *testing.T) {
	cb := &mockBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	// Provide RecorderState so the middleware can read the status.
	state, wrapped := NewRecorder(rec)
	ctx := WithRecorderState(req.Context(), state)
	req = req.WithContext(ctx)

	handler.ServeHTTP(wrapped, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, cb.doneInvoked, "done callback must be invoked")
	require.NotNil(t, cb.doneErr)
	assert.NoError(t, *cb.doneErr, "200 must report nil error (success)")
}

func TestCircuitBreaker_Open_Returns503(t *testing.T) {
	cb := &mockBreaker{open: true}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called when circuit is open")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj := body["error"].(map[string]any)
	assert.Equal(t, "ERR_CIRCUIT_OPEN", errObj["code"])
	assert.Equal(t, "service unavailable", errObj["message"],
		"503 message must say 'service unavailable', not 'internal server error'")
	assert.False(t, cb.doneInvoked, "done callback must not be invoked when circuit is open")
}

func TestCircuitBreaker_Standalone_NoRecorderState(t *testing.T) {
	cb := &mockBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// No RecorderState in context — middleware must create its own.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	require.True(t, cb.doneInvoked, "done callback must be invoked even without pre-existing RecorderState")
	assert.NoError(t, *cb.doneErr, "200 must report nil error (success)")
}

func TestCircuitBreaker_HandlerPanic_DoneStillCalled(t *testing.T) {
	cb := &mockBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("handler panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	state, wrapped := NewRecorder(rec)
	ctx := WithRecorderState(req.Context(), state)
	req = req.WithContext(ctx)

	assert.Panics(t, func() {
		handler.ServeHTTP(wrapped, req)
	}, "panic must propagate")

	require.True(t, cb.doneInvoked, "done callback must be invoked even when handler panics")
	require.NotNil(t, cb.doneErr)
	assert.Error(t, *cb.doneErr,
		"panic must always be recorded as failure, regardless of status code")
}

func TestCircuitBreaker_HandlerError5xx_ReportsFalse(t *testing.T) {
	cb := &mockBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	state, wrapped := NewRecorder(rec)
	ctx := WithRecorderState(req.Context(), state)
	req = req.WithContext(ctx)

	handler.ServeHTTP(wrapped, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	require.True(t, cb.doneInvoked, "done callback must be invoked")
	assert.Error(t, *cb.doneErr, "5xx must report non-nil error (failure)")
}

func TestCircuitBreaker_HandlerError4xx_ReportsTrue(t *testing.T) {
	cb := &mockBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	state, wrapped := NewRecorder(rec)
	ctx := WithRecorderState(req.Context(), state)
	req = req.WithContext(ctx)

	handler.ServeHTTP(wrapped, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	require.True(t, cb.doneInvoked, "done callback must be invoked")
	assert.NoError(t, *cb.doneErr, "4xx is a client error, not a server failure")
}

// mockBreakerWithRetryAfter implements both Allower and
// CircuitBreakerRetryAfter for testing the Retry-After header.
type mockBreakerWithRetryAfter struct {
	mockBreaker
	retryAfter time.Duration
}

func (m *mockBreakerWithRetryAfter) RetryAfter() time.Duration {
	return m.retryAfter
}

func TestCircuitBreaker_Open_RetryAfterHeader(t *testing.T) {
	cb := &mockBreakerWithRetryAfter{
		mockBreaker: mockBreaker{open: true},
		retryAfter:  30 * time.Second,
	}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Equal(t, "30", rec.Header().Get("Retry-After"),
		"Retry-After must reflect the circuit breaker timeout")
}

func TestCircuitBreaker_Open_NoRetryAfterWithoutInterface(t *testing.T) {
	cb := &mockBreaker{open: true}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler should not be called")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"),
		"Retry-After must not be set when policy does not implement CircuitBreakerRetryAfter")
}

func TestCircuitBreaker_NilBreaker_Panics(t *testing.T) {
	assert.Panics(t, func() {
		CircuitBreaker(nil)
	}, "nil Allower must panic at construction time")
}

// TestAllower_ISP verifies that a caller can depend only on Allower without
// needing to implement CircuitBreakerRetryAfter, demonstrating the ISP split.
func TestAllower_ISP(t *testing.T) {
	// allowerOnly implements only Allower — no RetryAfter.
	type allowerOnly struct{ mockBreaker }

	var _ Allower = (*allowerOnly)(nil) // compile-time check

	cb := &allowerOnly{mockBreaker: mockBreaker{open: true}}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("should not reach handler")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Empty(t, rec.Header().Get("Retry-After"),
		"Allower-only implementation must not set Retry-After")
}

func TestCircuitBreaker_204_ReportsSuccess(t *testing.T) {
	// 204 No Content is a success status; done must receive nil error.
	cb := &mockBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/resource/1", nil)
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, cb.doneInvoked)
	assert.NoError(t, *cb.doneErr, "204 must report nil error (success)")
}
