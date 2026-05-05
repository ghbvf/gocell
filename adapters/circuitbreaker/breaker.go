package circuitbreaker

// ref: sony/gobreaker v2 generation+expiry state machine — adopted as the
// model after we removed the third-party dependency. See ADR
// docs/architecture/202605021500-adr-kernel-clock-injection.md
// (D6 PROD-CLOCK-INJECTION-01) for the clock-injection invariant.

import (
	"errors"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	// ErrAdapterCircuitBreakerConfig signals invalid circuitbreaker.Config
	// at construction time (e.g. Name empty). Distinct from
	// ErrValidationFailed (HTTP request-parameter validation) so operators
	// can route adapter-construction failures separately from request
	// validation failures.
	ErrAdapterCircuitBreakerConfig errcode.Code = "ERR_ADAPTER_CIRCUIT_BREAKER_CONFIG"

	// defaultCircuitBreakerTimeout is the default open-state timeout when none
	// is provided in Config. Matches sony/gobreaker's internal default.
	defaultCircuitBreakerTimeout = 60 * time.Second
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

// Counts holds the counts of requests and their outcomes. It is a local type
// so callers do not need to import any third-party breaker package.
type Counts struct {
	Requests             uint32
	TotalSuccesses       uint32
	TotalFailures        uint32
	ConsecutiveSuccesses uint32
	ConsecutiveFailures  uint32
}

// Config holds settings for the circuit breaker adapter.
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

	// IsSuccessful classifies the error returned by the done callback as
	// success (return true) or failure (return false). When nil, the default
	// behavior is err == nil → success, err != nil → failure, which matches
	// sony/gobreaker's built-in default.
	//
	// Provide this field when the caller uses domain-specific sentinel errors
	// to distinguish expected non-fatal errors (e.g. ErrNotFound) from true
	// server failures that should count against the circuit.
	//
	// ref: sony/gobreaker — Settings.IsSuccessful
	IsSuccessful func(err error) bool
}

// internal sentinel errors used only within beforeRequest; never exported.
var (
	errOpenState       = errors.New("circuitbreaker: open")
	errTooManyRequests = errors.New("circuitbreaker: too many requests in half-open")
)

// breaker is the internal generation+expiry state machine.
// All methods that read or write state fields (other than the immutable config
// fields) assume the caller holds b.mu.
type breaker struct {
	mu         sync.Mutex
	state      State
	generation uint64
	counts     Counts
	expiry     time.Time

	// immutable after construction
	clk           clock.Clock
	name          string
	maxRequests   uint32
	interval      time.Duration
	timeout       time.Duration
	readyToTrip   func(Counts) bool
	isSuccessful  func(error) bool
	onStateChange func(string, State, State)
}

// currentState lazily transitions the breaker state based on expiry.
// Caller must hold b.mu.
func (b *breaker) currentState(now time.Time) (State, uint64) {
	switch b.state {
	case StateClosed:
		if !b.expiry.IsZero() && b.expiry.Before(now) {
			b.toNewGeneration(now)
		}
	case StateOpen:
		if b.expiry.Before(now) {
			b.setState(StateHalfOpen, now)
		}
	}
	return b.state, b.generation
}

// beforeRequest checks whether a request is permitted and increments Requests.
func (b *breaker) beforeRequest() (uint64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	state, gen := b.currentState(now)
	if state == StateOpen {
		return gen, errOpenState
	}
	if state == StateHalfOpen && b.counts.Requests >= b.maxRequests {
		return gen, errTooManyRequests
	}
	b.counts.Requests++
	return gen, nil
}

// afterRequest records the outcome of a request. Cross-generation done
// callbacks are silently ignored (generation fencing).
func (b *breaker) afterRequest(before uint64, success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clk.Now()
	state, gen := b.currentState(now)
	if gen != before {
		return // cross-generation callback — stale, ignore
	}
	if success {
		b.onSuccess(state, now)
	} else {
		b.onFailure(state, now)
	}
}

// onSuccess updates counts for a successful request.
// Caller must hold b.mu.
func (b *breaker) onSuccess(state State, now time.Time) {
	b.counts.TotalSuccesses++
	b.counts.ConsecutiveSuccesses++
	b.counts.ConsecutiveFailures = 0
	if state == StateHalfOpen && b.counts.ConsecutiveSuccesses >= b.maxRequests {
		b.setState(StateClosed, now)
	}
}

// onFailure updates counts for a failed request.
// Caller must hold b.mu.
func (b *breaker) onFailure(state State, now time.Time) {
	b.counts.TotalFailures++
	b.counts.ConsecutiveFailures++
	b.counts.ConsecutiveSuccesses = 0
	switch state {
	case StateClosed:
		if b.readyToTrip(b.counts) {
			b.setState(StateOpen, now)
		}
	case StateHalfOpen:
		b.setState(StateOpen, now)
	}
}

// setState transitions to a new state and starts a fresh generation.
// Caller must hold b.mu.
func (b *breaker) setState(target State, now time.Time) {
	if b.state == target {
		return
	}
	prev := b.state
	b.state = target
	b.toNewGeneration(now)
	if b.onStateChange != nil {
		b.onStateChange(b.name, prev, target)
	}
}

// toNewGeneration bumps the generation counter, resets counts, and sets
// the expiry for the new state.
// Caller must hold b.mu.
func (b *breaker) toNewGeneration(now time.Time) {
	b.generation++
	b.counts = Counts{}
	switch b.state {
	case StateClosed:
		if b.interval > 0 {
			b.expiry = now.Add(b.interval)
		} else {
			b.expiry = time.Time{}
		}
	case StateOpen:
		b.expiry = now.Add(b.timeout)
	case StateHalfOpen:
		b.expiry = time.Time{}
	}
}

// Adapter implements middleware.Allower and middleware.CircuitBreakerRetryAfter
// using an in-house three-state circuit breaker.
type Adapter struct {
	cb      *breaker
	timeout time.Duration
}

// New creates a circuit breaker adapter. It panics if clk is nil (PROD-CLOCK-INJECTION-01).
// Returns an error if cfg.Name is empty, as Name is required for logs and
// metrics identification. Production configurations must never silently degrade.
func New(cfg Config, clk clock.Clock) (*Adapter, error) {
	clock.MustHaveClock(clk, "circuitbreaker.New")
	if cfg.Name == "" {
		return nil, errcode.New(errcode.KindInvalid, ErrAdapterCircuitBreakerConfig,
			"circuitbreaker: Name required")
	}
	maxReq := cfg.MaxRequests
	if maxReq == 0 {
		maxReq = 1
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultCircuitBreakerTimeout
	}
	readyToTrip := cfg.ReadyToTrip
	if readyToTrip == nil {
		readyToTrip = func(c Counts) bool { return c.ConsecutiveFailures > 5 }
	}
	isSuccessful := cfg.IsSuccessful
	if isSuccessful == nil {
		isSuccessful = func(err error) bool { return err == nil }
	}
	b := &breaker{
		state:         StateClosed,
		clk:           clk,
		name:          cfg.Name,
		maxRequests:   maxReq,
		interval:      cfg.Interval,
		timeout:       timeout,
		readyToTrip:   readyToTrip,
		isSuccessful:  isSuccessful,
		onStateChange: cfg.OnStateChange,
	}
	b.toNewGeneration(clk.Now())
	return &Adapter{cb: b, timeout: timeout}, nil
}

// Allow checks if the request should proceed. Returns allowed=true and a done
// callback when the circuit is closed or half-open. The done callback MUST be
// called exactly once with nil (success) or a non-nil error (failure). Returns
// allowed=false and a nil done when the circuit is open.
func (a *Adapter) Allow() (allowed bool, done func(err error)) {
	gen, err := a.cb.beforeRequest()
	if err != nil {
		return false, nil
	}
	return true, func(err error) {
		a.cb.afterRequest(gen, a.cb.isSuccessful(err))
	}
}

// RetryAfter returns the open-state timeout — the duration until the circuit
// may transition to half-open and accept probe requests. The middleware uses
// this to set the Retry-After header on 503 responses (RFC 7231 Section 7.1.3).
func (a *Adapter) RetryAfter() time.Duration {
	return a.timeout
}

// State returns the current state of the circuit breaker.
func (a *Adapter) State() State {
	a.cb.mu.Lock()
	defer a.cb.mu.Unlock()
	state, _ := a.cb.currentState(a.cb.clk.Now())
	return state
}
