package circuitbreaker

// ref: sony/gobreaker v2 generation+expiry state machine — adopted as the
// model after we removed the third-party dependency. See ADR
// docs/architecture/202605021500-adr-kernel-clock-injection.md
// (D6 PROD-CLOCK-INJECTION-01) for the clock-injection invariant.

import (
	"errors"
	"log/slog"
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

// Config holds settings for the circuitbreaker adapter.
//
// Name is the only required field; all other fields use built-in defaults
// when zero (see each field's doc). New() returns ErrAdapterCircuitBreakerConfig
// when Name is empty; other invalid values are silently coerced to defaults
// to keep ad-hoc constructions ergonomic.
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
	//
	// The callback is invoked OUTSIDE the breaker mutex, so it may safely
	// call any Adapter method (Allow, State) without risk of reentrant
	// deadlock. A panic in the callback propagates to the caller of Allow
	// or the done callback; the state machine remains consistent because
	// callbacks fired during beforeRequest run before slot accounting (no
	// stranded half-open probes), and callbacks fired during afterRequest /
	// State run after counts have already been reset by toNewGeneration.
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

// stateTransition records a single state change that occurred inside a
// critical section. The slice is passed out of the lock so the caller can
// fire user callbacks and slog after Unlock.
//
// ref: hashicorp/raft event bus — collect events during critical section,
// fire after Unlock to avoid reentrant deadlock.
type stateTransition struct {
	name string
	prev State
	next State
}

// currentState lazily transitions the breaker state based on expiry.
// It appends any transition to *ts (caller-allocated).
// Caller must hold b.mu.
func (b *breaker) currentState(now time.Time, ts *[]stateTransition) (State, uint64) {
	switch b.state {
	case StateClosed:
		if !b.expiry.IsZero() && b.expiry.Before(now) {
			b.toNewGeneration(now)
		}
	case StateOpen:
		if !b.expiry.IsZero() && b.expiry.Before(now) {
			b.setState(StateHalfOpen, now, ts)
		}
	}
	return b.state, b.generation
}

// beforeRequest checks whether a request is permitted and increments Requests.
//
// Implementation note: this is a two-pass design.
//   - Phase 1: take the lock briefly to read state and collect any pending
//     lazy transition (open→half-open). Do NOT mutate counts.
//   - Phase 2: release the lock and fire OnStateChange callbacks. Outside the
//     lock so the callback may safely call Allow/State without reentrant
//     deadlock; before counts.Requests++ so a panic in the callback can never
//     strand a half-open probe slot (counts stays at 0 for any new generation).
//   - Phase 3: re-take the lock to gate and account. If the callback advanced
//     state in Phase 2 (gen mismatch), reject this request conservatively;
//     the caller's done callback (if any) is no-op via afterRequest's gen fence.
func (b *breaker) beforeRequest() (uint64, error) {
	// Phase 1: read state + collect transitions, no counts mutation.
	var transitions []stateTransition
	b.mu.Lock()
	now := b.clk.Now()
	state, gen := b.currentState(now, &transitions)
	b.mu.Unlock()

	// Phase 2: fire callbacks outside the lock and before slot accounting.
	b.fireTransitions(transitions)

	// Phase 3: re-lock to commit. Generation fence guards against state
	// advancement triggered by the callback in Phase 2.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.generation != gen {
		// Callback advanced state. Phase 1 observation is stale; reject
		// conservatively so the caller retries against the new state.
		return b.generation, errOpenState
	}
	// gen unchanged ⇒ state unchanged ⇒ Phase 1 observation still valid.
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
	var ts []stateTransition
	b.mu.Lock()
	now := b.clk.Now()
	state, gen := b.currentState(now, &ts)
	if gen == before {
		if success {
			b.onSuccess(state, now, &ts)
		} else {
			b.onFailure(state, now, &ts)
		}
	}
	// else: cross-generation callback — stale, ignore
	b.mu.Unlock()
	b.fireTransitions(ts)
}

// onSuccess updates counts for a successful request.
// Caller must hold b.mu.
func (b *breaker) onSuccess(state State, now time.Time, ts *[]stateTransition) {
	b.counts.TotalSuccesses++
	b.counts.ConsecutiveSuccesses++
	b.counts.ConsecutiveFailures = 0
	if state == StateHalfOpen && b.counts.ConsecutiveSuccesses >= b.maxRequests {
		b.setState(StateClosed, now, ts)
	}
}

// onFailure updates counts for a failed request.
// Caller must hold b.mu.
func (b *breaker) onFailure(state State, now time.Time, ts *[]stateTransition) {
	b.counts.TotalFailures++
	b.counts.ConsecutiveFailures++
	b.counts.ConsecutiveSuccesses = 0
	switch state {
	case StateClosed:
		if b.readyToTrip(b.counts) {
			b.setState(StateOpen, now, ts)
		}
	case StateHalfOpen:
		b.setState(StateOpen, now, ts)
	}
}

// setState transitions to a new state and starts a fresh generation.
// It appends the transition to *ts; the caller fires callbacks after Unlock.
// Caller must hold b.mu.
func (b *breaker) setState(target State, now time.Time, ts *[]stateTransition) {
	if b.state == target {
		return
	}
	prev := b.state
	b.state = target
	b.toNewGeneration(now)
	*ts = append(*ts, stateTransition{name: b.name, prev: prev, next: target})
}

// fireTransitions emits a slog Info event and invokes the user OnStateChange
// callback for each collected transition. Called outside b.mu to avoid
// reentrant deadlock and (in beforeRequest) before counts mutation to avoid
// stranding probe slots on callback panic. A panic in the callback propagates
// to the caller of Allow / done; the state machine remains consistent.
func (b *breaker) fireTransitions(ts []stateTransition) {
	for _, t := range ts {
		slog.Info("circuitbreaker: state transition",
			"name", t.name,
			"from", t.prev.String(),
			"to", t.next.String())
		if b.onStateChange != nil {
			b.onStateChange(t.name, t.prev, t.next)
		}
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
// callback when the circuit is closed or has free probe slots in half-open.
// The done callback MUST be called exactly once with nil (success) or a non-nil
// error (failure). Returns allowed=false and a nil done when the circuit is
// open OR when half-open has reached MaxRequests in-flight probes — both
// rejection causes are indistinguishable to the caller; use State() to
// observe the current circuit state for diagnostics.
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
	var ts []stateTransition
	a.cb.mu.Lock()
	state, _ := a.cb.currentState(a.cb.clk.Now(), &ts)
	a.cb.mu.Unlock()
	a.cb.fireTransitions(ts)
	return state
}
