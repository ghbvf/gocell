package circuitbreaker

import (
	"errors"
	"time"

	"github.com/sony/gobreaker/v2"
)

// State represents the state of the circuit breaker.
type State int

const (
	// StateClosed means the circuit breaker is closed: requests flow through.
	StateClosed State = iota
	// StateHalfOpen means the circuit breaker is probing with a limited number of requests.
	StateHalfOpen
	// StateOpen means the circuit breaker is open: requests are rejected.
	StateOpen
)

// String returns a human-readable name for the state.
func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateHalfOpen:
		return "half-open"
	case StateOpen:
		return "open"
	default:
		return "unknown"
	}
}

// Counts holds the counts of requests and their outcomes. It mirrors
// gobreaker.Counts but is a local type so callers do not import gobreaker.
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

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
	ReadyToTrip func(counts Counts) bool

	// OnStateChange is called whenever the circuit state changes.
	OnStateChange func(name string, from, to State)
}

// Adapter wraps sony/gobreaker's TwoStepCircuitBreaker to implement
// middleware.Allower and middleware.CircuitBreakerRetryAfter.
type Adapter struct {
	cb      *gobreaker.TwoStepCircuitBreaker[struct{}]
	timeout time.Duration
}

// New creates a gobreaker-backed circuit breaker adapter.
func New(cfg Config) *Adapter {
	st := gobreaker.Settings{
		Name:        cfg.Name,
		MaxRequests: cfg.MaxRequests,
		Interval:    cfg.Interval,
		Timeout:     cfg.Timeout,
	}
	if cfg.ReadyToTrip != nil {
		fn := cfg.ReadyToTrip
		st.ReadyToTrip = func(c gobreaker.Counts) bool {
			return fn(Counts{
				Requests:             c.Requests,
				TotalSuccesses:       c.TotalSuccesses,
				TotalFailures:        c.TotalFailures,
				ConsecutiveSuccesses: c.ConsecutiveSuccesses,
				ConsecutiveFailures:  c.ConsecutiveFailures,
			})
		}
	}
	if cfg.OnStateChange != nil {
		fn := cfg.OnStateChange
		st.OnStateChange = func(name string, from, to gobreaker.State) {
			fn(name, gobreakerState(from), gobreakerState(to))
		}
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

// gobreakerState converts a gobreaker.State to the local State type.
func gobreakerState(s gobreaker.State) State {
	switch s {
	case gobreaker.StateClosed:
		return StateClosed
	case gobreaker.StateHalfOpen:
		return StateHalfOpen
	case gobreaker.StateOpen:
		return StateOpen
	default:
		return StateClosed
	}
}

// Allow checks if the request should proceed. Returns allowed=true and a done
// callback when the circuit is closed or half-open. The done callback MUST be
// called exactly once with nil (success) or a non-nil error (failure). Returns
// allowed=false and a nil done when the circuit is open.
func (a *Adapter) Allow() (allowed bool, done func(err error)) {
	gobDone, err := a.cb.Allow()
	if err != nil {
		return false, nil
	}
	return true, func(err error) {
		if err == nil {
			gobDone(nil)
		} else {
			gobDone(errServerFailure)
		}
	}
}

// RetryAfter returns the open-state timeout — the duration until the circuit
// may transition to half-open and accept probe requests. The middleware uses
// this to set the Retry-After header on 503 responses (RFC 7231 Section 7.1.3).
func (a *Adapter) RetryAfter() time.Duration {
	return a.timeout
}

// State returns the current state of the circuit breaker using the local State
// type (no gobreaker import required by callers).
func (a *Adapter) State() State {
	return gobreakerState(a.cb.State())
}
