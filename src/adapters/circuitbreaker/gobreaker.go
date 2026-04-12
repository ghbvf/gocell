package circuitbreaker

import (
	"errors"
	"time"

	"github.com/sony/gobreaker/v2"

	"github.com/ghbvf/gocell/runtime/http/middleware"
)

// Compile-time check: Adapter implements CircuitBreakerPolicy.
var _ middleware.CircuitBreakerPolicy = (*Adapter)(nil)

// errServerFailure is the sentinel error passed to gobreaker's done callback
// when the HTTP handler reports a server-side failure (5xx status).
var errServerFailure = errors.New("server failure")

// Config holds settings for the gobreaker adapter.
type Config struct {
	// Name identifies the circuit breaker instance (required, used in logs/metrics).
	Name string

	// MaxRequests is the maximum number of requests allowed to pass through
	// when the circuit breaker is half-open. Default: 1.
	MaxRequests uint32

	// Interval is the cyclic period of the closed state for clearing internal
	// counts. Default: 0 (never clears in closed state).
	Interval time.Duration

	// Timeout is the duration of the open state, after which the circuit
	// transitions to half-open. Default: 60s.
	Timeout time.Duration

	// ReadyToTrip is called with counts whenever a request fails in the closed
	// state. If it returns true, the circuit opens. Default: consecutive
	// failures > 5.
	ReadyToTrip func(counts gobreaker.Counts) bool

	// OnStateChange is called whenever the circuit state changes.
	OnStateChange func(name string, from, to gobreaker.State)
}

// Compile-time check: Adapter implements CircuitBreakerRetryAfter.
var _ middleware.CircuitBreakerRetryAfter = (*Adapter)(nil)

// Adapter wraps sony/gobreaker's TwoStepCircuitBreaker to implement
// middleware.CircuitBreakerPolicy and middleware.CircuitBreakerRetryAfter.
type Adapter struct {
	cb      *gobreaker.TwoStepCircuitBreaker[struct{}]
	timeout time.Duration
}

// New creates a gobreaker-backed circuit breaker adapter.
func New(cfg Config) *Adapter {
	st := gobreaker.Settings{
		Name:          cfg.Name,
		MaxRequests:   cfg.MaxRequests,
		Interval:      cfg.Interval,
		Timeout:       cfg.Timeout,
		ReadyToTrip:   cfg.ReadyToTrip,
		OnStateChange: cfg.OnStateChange,
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second // gobreaker default
	}
	return &Adapter{
		cb:      gobreaker.NewTwoStepCircuitBreaker[struct{}](st),
		timeout: timeout,
	}
}

// Allow checks if the request should proceed. Returns a done callback that
// must be called with true (success) or false (failure). If the circuit is
// open, returns (nil, error).
func (a *Adapter) Allow() (func(success bool), error) {
	done, err := a.cb.Allow()
	if err != nil {
		return nil, err
	}
	return func(success bool) {
		if success {
			done(nil)
		} else {
			done(errServerFailure)
		}
	}, nil
}

// RetryAfter returns the open-state timeout — the duration until the circuit
// may transition to half-open and accept probe requests. The middleware uses
// this to set the Retry-After header on 503 responses (RFC 7231 Section 7.1.3).
func (a *Adapter) RetryAfter() time.Duration {
	return a.timeout
}

// State returns the current state of the circuit breaker.
func (a *Adapter) State() gobreaker.State {
	return a.cb.State()
}
