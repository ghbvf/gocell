package middleware

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"reflect"
	"strconv"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// errServerFailure is the sentinel error reported to the circuit breaker done
// callback when the HTTP handler signals a server-side failure (5xx status or
// panic). Using a non-nil error (vs. a bool) aligns with the Allower contract
// where nil = success and any error = failure.
var errServerFailure = errors.New("server failure")

// CircuitBreakerRetryAfter is an optional interface that Allower
// implementations can satisfy to provide Retry-After guidance on 503 responses.
// When implemented, the middleware sets the Retry-After header so clients know
// when to retry (RFC 7231 Section 7.1.3).
type CircuitBreakerRetryAfter interface {
	// RetryAfter returns the suggested duration until the circuit may allow
	// requests again (typically the open-state timeout).
	RetryAfter() time.Duration
}

// Allower is the ISP-minimal interface required by the CircuitBreaker
// middleware. It covers only the "gate-and-report" concern of a two-step
// circuit breaker, decoupled from state inspection or statistics.
//
// Callers that need state inspection (e.g. health checks) should depend on the
// concrete type or a richer interface defined in their own package.
//
// ref: sony/gobreaker — TwoStepCircuitBreaker Allow/done(err) protocol
// ref: go-kratos/aegis — circuitbreaker.CircuitBreaker interface
type Allower interface {
	// Allow checks if the request should proceed.
	//
	// If the circuit is closed or half-open, Allow returns allowed=true and a
	// non-nil done callback that MUST be called exactly once with nil (success)
	// or a non-nil error (failure).
	//
	// If the circuit is open, Allow returns allowed=false and a nil done.
	Allow() (allowed bool, done func(err error))
}

// IsTypedNilAllower reports whether cb is a typed-nil pointer wrapped in an
// Allower interface. A typed-nil passes a plain cb == nil check because the
// interface value is non-nil (it carries type information), but calling any
// method on the underlying pointer will panic.
//
// Usage: call this after the cb == nil interface check, so the fast path still
// short-circuits on a bare nil interface:
//
//	if cb == nil || middleware.IsTypedNilAllower(cb) {
//	    // reject
//	}
//
// ref: golang.org/src/reflect Value.IsNil — kind-gated to avoid panic on
// non-nilable kinds (string, int, struct, …).
func IsTypedNilAllower(cb Allower) bool {
	v := reflect.ValueOf(cb)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map,
		reflect.Slice, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	return false
}

// CircuitBreaker returns HTTP middleware that protects upstream handlers using
// the given Allower. When the circuit is open, requests are rejected with 503
// Service Unavailable. When closed or half-open, requests proceed to the next
// handler; the response status determines success/failure reporting (5xx =
// failure, everything else = success).
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
func CircuitBreaker(cb Allower) func(http.Handler) http.Handler {
	if cb == nil {
		panic("middleware: Allower must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			circuitBreakerServe(cb, next, w, r)
		})
	}
}

// circuitBreakerServe is the per-request handler for the CircuitBreaker
// middleware. Extracted to keep CircuitBreaker's cognitive complexity ≤ 15.
func circuitBreakerServe(cb Allower, next http.Handler, w http.ResponseWriter, r *http.Request) {
	allowed, done := cb.Allow()
	if !allowed {
		writeCircuitOpenError(w, r, cb)
		return
	}

	// Guard against Allower implementations that violate the contract by
	// returning allowed=true with a nil done callback. Without this guard a
	// deferred done(...) call would panic with a nil function pointer, causing
	// an unrecoverable 500. Fail open with a no-op so the request is served,
	// and log an Error so the operator can detect the broken implementation.
	if done == nil {
		slog.ErrorContext(r.Context(), "circuitbreaker: Allow returned nil done, contract violation; failing open")
		done = func(error) {}
	}

	state, w, r := ensureRecorder(w, r)

	// Use recover to guarantee done(err) on panic, then re-panic.
	// Without this, a standalone breaker (no Recovery middleware) would
	// see the default status 200 and record success for a crashing handler.
	// With Recovery in the chain, Recovery catches first and writes 500,
	// so recover() here returns nil and we fall through to status-based logic.
	//
	// ref: sony/gobreaker — Execute treats panic as failure
	// ref: go-kit/kit circuitbreaker — panic propagates after failure recording
	defer func() {
		if p := recover(); p != nil {
			done(errServerFailure)
			panic(p)
		}
		if state.Status() >= 500 {
			done(errServerFailure)
		} else {
			done(nil)
		}
	}()

	next.ServeHTTP(w, r)
}

// ensureRecorder returns the existing RecorderState from context, or creates a
// new one and wraps w. Returns the state, the (possibly wrapped) ResponseWriter,
// and the (possibly updated) request.
func ensureRecorder(w http.ResponseWriter, r *http.Request) (*RecorderState, http.ResponseWriter, *http.Request) {
	state := RecorderStateFrom(r.Context())
	if state != nil {
		return state, w, r
	}
	var wrapped http.ResponseWriter
	state, wrapped = NewRecorder(w)
	ctx := WithRecorderState(r.Context(), state)
	return state, wrapped, r.WithContext(ctx)
}

// writeCircuitOpenError writes a 503 response with the ERR_CIRCUIT_OPEN code.
// Uses httputil.WritePublicError so the message "service unavailable" is
// preserved (not masked to "internal server error"), while inheriting the
// canonical error envelope format.
//
// If the policy implements CircuitBreakerRetryAfter, the Retry-After header
// is set per RFC 7231 Section 7.1.3.
func writeCircuitOpenError(w http.ResponseWriter, r *http.Request, cb Allower) {
	// Set Retry-After if the policy provides it. Must be set before
	// WritePublicError calls w.WriteHeader.
	if ra, ok := cb.(CircuitBreakerRetryAfter); ok {
		if d := ra.RetryAfter(); d > 0 {
			secs := int(math.Ceil(d.Seconds()))
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
	}

	httputil.WritePublicError(r.Context(), w, http.StatusServiceUnavailable,
		string(errcode.ErrCircuitOpen), "service unavailable")
}
