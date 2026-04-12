package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// CircuitBreakerPolicy abstracts a circuit breaker's two-step protocol.
// Implementations should follow the Closed -> Open -> Half-Open -> Closed
// state machine pattern.
//
// ref: sony/gobreaker — TwoStepCircuitBreaker Allow/done(success) protocol
// ref: go-kratos/aegis — circuitbreaker.CircuitBreaker interface
type CircuitBreakerPolicy interface {
	// Allow checks if the request should proceed. If allowed, it returns a
	// non-nil done callback that MUST be called exactly once with the outcome
	// (true = success, false = failure). If the circuit is open, Allow returns
	// a nil done and a non-nil error.
	Allow() (done func(success bool), err error)
}

// CircuitBreaker returns HTTP middleware that protects upstream handlers using
// the given CircuitBreakerPolicy. When the circuit is open, requests are
// rejected with 503 Service Unavailable. When closed or half-open, requests
// proceed to the next handler; the response status determines success/failure
// reporting (5xx = failure, everything else = success).
//
// The middleware reuses an existing RecorderState from context (created by the
// Recorder middleware). If none exists, it creates its own so it remains
// usable as a standalone middleware.
//
// ref: sony/gobreaker — TwoStepCircuitBreaker for HTTP request protection
// ref: go-kit/kit circuitbreaker — middleware wrapping pattern
func CircuitBreaker(cb CircuitBreakerPolicy) func(http.Handler) http.Handler {
	if cb == nil {
		panic("middleware: CircuitBreakerPolicy must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			done, err := cb.Allow()
			if err != nil {
				httputil.WriteError(r.Context(), w, http.StatusServiceUnavailable,
					"ERR_CIRCUIT_OPEN", "service unavailable")
				return
			}

			state := RecorderStateFrom(r.Context())
			if state == nil {
				var wrapped http.ResponseWriter
				state, wrapped = NewRecorder(w)
				w = wrapped
				ctx := WithRecorderState(r.Context(), state)
				r = r.WithContext(ctx)
			}

			next.ServeHTTP(w, r)
			done(state.Status() < 500)
		})
	}
}
