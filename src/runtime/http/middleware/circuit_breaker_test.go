package middleware

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBreaker implements CircuitBreakerPolicy for testing.
type mockBreaker struct {
	allowErr    error
	doneSuccess *bool // captures the value passed to done()
}

func (m *mockBreaker) Allow() (func(bool), error) {
	if m.allowErr != nil {
		return nil, m.allowErr
	}
	return func(success bool) { m.doneSuccess = &success }, nil
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
	require.NotNil(t, cb.doneSuccess, "done callback must be invoked")
	assert.True(t, *cb.doneSuccess, "200 must report success")
}

func TestCircuitBreaker_Open_Returns503(t *testing.T) {
	cb := &mockBreaker{allowErr: errors.New("circuit breaker is open")}
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
	assert.Nil(t, cb.doneSuccess, "done callback must not be invoked when circuit is open")
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
	require.NotNil(t, cb.doneSuccess, "done callback must be invoked even without pre-existing RecorderState")
	assert.True(t, *cb.doneSuccess, "200 must report success")
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

	require.NotNil(t, cb.doneSuccess, "done callback must be invoked even when handler panics")
	// Default RecorderState status is 200 (no WriteHeader called before panic).
	// In the real chain, Recovery sets 500 before this point.
	assert.True(t, *cb.doneSuccess,
		"without Recovery, panic-before-WriteHeader leaves status at default 200")
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
	require.NotNil(t, cb.doneSuccess, "done callback must be invoked")
	assert.False(t, *cb.doneSuccess, "5xx must report failure")
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
	require.NotNil(t, cb.doneSuccess, "done callback must be invoked")
	assert.True(t, *cb.doneSuccess, "4xx is a client error, not a server failure")
}

func TestCircuitBreaker_NilBreaker_Panics(t *testing.T) {
	assert.Panics(t, func() {
		CircuitBreaker(nil)
	}, "nil CircuitBreakerPolicy must panic at construction time")
}
