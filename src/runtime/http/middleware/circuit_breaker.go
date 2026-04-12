package middleware

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// CircuitBreakerRetryAfter is an optional interface that CircuitBreakerPolicy
// implementations can satisfy to provide Retry-After guidance on 503 responses.
// When implemented, the middleware sets the Retry-After header so clients know
// when to retry (RFC 7231 Section 7.1.3).
type CircuitBreakerRetryAfter interface {
	// RetryAfter returns the suggested duration until the circuit may allow
	// requests again (typically the open-state timeout).
	RetryAfter() time.Duration
}

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
// The done callback is invoked via defer to guarantee it is called even when
// the downstream handler panics (the panic is re-raised after reporting).
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
				writeCircuitOpenError(w, r, cb)
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

			// Defer done callback so it fires even when the handler panics.
			// Recovery middleware (further down the chain) catches the panic
			// and sets status 500, but if there is no Recovery, we still need
			// the breaker to record the failure before the panic propagates.
			defer func() {
				done(state.Status() < 500)
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// writeCircuitOpenError writes a 503 response with the ERR_CIRCUIT_OPEN code.
// This bypasses httputil.WriteError because that function masks all 5xx
// messages to "internal server error". For a circuit breaker, "service
// unavailable" is the correct client-facing message — it indicates a
// deliberate protective action, not a bug.
//
// If the policy implements CircuitBreakerRetryAfter, the Retry-After header
// is set per RFC 7231 Section 7.1.3.
func writeCircuitOpenError(w http.ResponseWriter, r *http.Request, cb CircuitBreakerPolicy) {
	// Set Retry-After if the policy provides it.
	if ra, ok := cb.(CircuitBreakerRetryAfter); ok {
		if d := ra.RetryAfter(); d > 0 {
			secs := int(math.Ceil(d.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
	}

	body := map[string]any{
		"code":    string(errcode.ErrCircuitOpen),
		"message": "service unavailable",
		"details": map[string]any{},
	}
	if rid, ok := ctxkeys.RequestIDFrom(r.Context()); ok {
		body["request_id"] = rid
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}
