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

// TestIsTypedNilAllower verifies that IsTypedNilAllower detects typed-nil
// pointers wrapped in an Allower interface value.
func TestIsTypedNilAllower(t *testing.T) {
	cases := []struct {
		name string
		cb   Allower
		want bool
	}{
		{
			name: "nil interface value",
			cb:   nil,
			want: false, // bare nil interface: cb == nil already catches this
		},
		{
			name: "typed-nil pointer",
			cb:   (*mockBreaker)(nil),
			want: true,
		},
		{
			name: "valid non-nil pointer",
			cb:   &mockBreaker{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsTypedNilAllower(tc.cb)
			assert.Equal(t, tc.want, got)
		})
	}
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

func TestCircuitBreaker_3xx_ReportsSuccess(t *testing.T) {
	// 3xx redirect responses are not server failures; done must receive nil error.
	cases := []struct {
		name string
		code int
	}{
		{"301 Moved Permanently", http.StatusMovedPermanently},
		{"302 Found", http.StatusFound},
		{"304 Not Modified", http.StatusNotModified},
		{"307 Temporary Redirect", http.StatusTemporaryRedirect},
		{"308 Permanent Redirect", http.StatusPermanentRedirect},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			cb := &mockBreaker{}
			handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
			}))

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			state, wrapped := NewRecorder(rec)
			ctx := WithRecorderState(req.Context(), state)
			req = req.WithContext(ctx)

			handler.ServeHTTP(wrapped, req)

			assert.Equal(t, tc.code, rec.Code)
			require.True(t, cb.doneInvoked, "done callback must be invoked for %d", tc.code)
			require.NotNil(t, cb.doneErr)
			assert.NoError(t, *cb.doneErr, "%d must report nil error (success, not a server failure)", tc.code)
		})
	}
}

// statefulMockBreaker is a test double that transitions to open after a
// failure is reported via the done callback, modelling the full
// Allow → done(err) → Allow state-machine path.
type statefulMockBreaker struct {
	open        bool
	doneErr     *error
	doneInvoked bool
}

func (s *statefulMockBreaker) Allow() (bool, func(error)) {
	if s.open {
		return false, nil
	}
	return true, func(err error) {
		s.doneInvoked = true
		s.doneErr = &err
		if err != nil {
			s.open = true // simulate circuit opening after failure
		}
	}
}

func TestCircuitBreaker_StateMachineTransition_AllowDoneAllow(t *testing.T) {
	// Verifies that done(err) from a 5xx response causes the same breaker
	// instance to reject the next Allow() — state propagates correctly.
	sb := &statefulMockBreaker{}

	// First request: circuit closed, handler returns 500, done receives non-nil error.
	handler1 := CircuitBreaker(sb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodGet, "/", nil)
	state1, wrapped1 := NewRecorder(rec1)
	ctx1 := WithRecorderState(req1.Context(), state1)
	req1 = req1.WithContext(ctx1)
	handler1.ServeHTTP(wrapped1, req1)

	assert.Equal(t, http.StatusInternalServerError, rec1.Code)
	require.True(t, sb.doneInvoked, "done must be invoked after first request")
	require.NotNil(t, sb.doneErr)
	assert.Error(t, *sb.doneErr, "5xx must pass non-nil error to done")

	// Second request: same breaker instance is now open — must return 503.
	handler2 := CircuitBreaker(sb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler must not be called when circuit is open")
	}))
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	handler2.ServeHTTP(rec2, req2)

	assert.Equal(t, http.StatusServiceUnavailable, rec2.Code,
		"second request must be rejected after breaker opens from failure")
}

// nilDoneBreaker returns allowed=true but a nil done callback, violating the
// Allower contract. Used to test the nil-done guard in circuitBreakerServe.
type nilDoneBreaker struct{}

func (n *nilDoneBreaker) Allow() (bool, func(error)) {
	return true, nil
}

// TestCircuitBreaker_AllowerReturnsNilDone_NoPanic verifies that when an
// Allower implementation violates the contract by returning allowed=true with a
// nil done callback, the middleware fails open (serves the request, returns
// 200) without panicking.
func TestCircuitBreaker_AllowerReturnsNilDone_NoPanic(t *testing.T) {
	cb := &nilDoneBreaker{}
	handler := CircuitBreaker(cb)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	// Must not panic even though done is nil.
	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	}, "nil done must not cause a panic")

	assert.Equal(t, http.StatusOK, rec.Code, "request must be served (fail-open)")
}
