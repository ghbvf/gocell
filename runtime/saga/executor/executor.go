// Package executor provides the step-level execution engine for saga instances.
//
// Executor drives a single step of a saga definition: it runs the step function
// with retry/backoff, manages per-step timeouts, and maintains the instance
// lease via periodic heartbeats. It does NOT own the saga state machine
// (that belongs to the Coordinator in runtime/saga) and does NOT write journal
// events (it returns a Result that the caller persists).
//
// # Heartbeater interface
//
// Executor accepts a narrow Heartbeater interface rather than the full
// journal.Journal type. This satisfies SAGA-JOURNAL-HOLDER-SEAL-01: the
// executor package never imports kernel/saga/journal. Any journal.Journal
// implementation structurally satisfies Heartbeater.
//
// # StepFunc call funnel (SAGA-STEP-RUN-OUTSIDE-TX-01 A1)
//
// All ksaga.StepFunc invocations occur exclusively inside the unexported
// safeRun helper. Execute passes step.Run as a parameter to safeRun; it
// never calls step.Run(…) directly. This satisfies the A1 invariant checked
// by the archtest.
package executor

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Outcome classifies the result of an Execute call.
// The zero value is intentionally invalid — callers must check for known
// Outcome constants, not treat zero as a sentinel.
type Outcome uint8

const (
	// OutcomeSucceeded means the step's Run returned a nil error.
	OutcomeSucceeded Outcome = iota + 1
	// OutcomeFailed means retry attempts were exhausted and there is no
	// Compensate function (or it is intentionally nil).
	OutcomeFailed
	// OutcomeExpired means the step or parent context deadline was exceeded.
	OutcomeExpired
	// OutcomeCompensationRequired means retries were exhausted and the step
	// has a non-nil Compensate function. The Coordinator should call
	// Executor.Compensate for this step.
	//
	// Deprecated (RED-stub, removed in GREEN): the executor must not pre-empt
	// the Coordinator's Compensating-vs-Failed decision; kept only so the RED
	// commit compiles while the new behavior tests fail.
	OutcomeCompensationRequired
	// OutcomeCanceled means the parent context was explicitly canceled
	// (orchestrator shutdown/abort) — NOT a business expiry. The Coordinator
	// should leave the instance for re-claim, not terminate it.
	OutcomeCanceled
	// OutcomeLeaseLost means a heartbeat observed ok=false: another coordinator
	// now owns the lease. The running step was canceled.
	OutcomeLeaseLost
)

// String implements fmt.Stringer for readable log output.
func (o Outcome) String() string {
	switch o {
	case OutcomeSucceeded:
		return "Succeeded"
	case OutcomeFailed:
		return "Failed"
	case OutcomeExpired:
		return "Expired"
	case OutcomeCompensationRequired:
		return "CompensationRequired"
	case OutcomeCanceled:
		return "Canceled"
	case OutcomeLeaseLost:
		return "LeaseLost"
	default:
		return "Outcome(" + strconv.Itoa(int(o)) + ")"
	}
}

// Result is the value returned by Execute.
type Result struct {
	// Outcome classifies how Execute ended.
	Outcome Outcome
	// NewState is the opaque JSON state returned by the step's Run function
	// on success (nil on failure/expiry).
	NewState []byte
	// Err is the last error encountered (nil on OutcomeSucceeded).
	Err error
	// Attempts is the total number of Run invocations made.
	Attempts int
}

// Executor drives a single step of a saga.
type Executor struct {
	heartbeater       Heartbeater
	clk               clock.Clock
	heartbeatInterval time.Duration
	leaseDuration     time.Duration
	jitter            jitterSource
	logger            *slog.Logger
}

// NewExecutor constructs an Executor. hb and clk are required:
//   - hb nil (or typed-nil) → errcode.KindInvalid
//   - clk nil → panics via clock.MustHaveClock (programmer error)
//
// Options are applied after defaults; post-option validation checks:
//   - heartbeatInterval > 0
//   - leaseDuration > 0
//   - heartbeatInterval * 2 < leaseDuration (guarantees at least one heartbeat before expiry)
func NewExecutor(hb Heartbeater, clk clock.Clock, opts ...Option) (*Executor, error) {
	if validation.IsNilInterface(hb) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga/executor: heartbeater required",
		)
	}
	clock.MustHaveClock(clk, "runtime/saga/executor.NewExecutor")

	e := &Executor{
		heartbeater:       hb,
		clk:               clk,
		heartbeatInterval: 10 * time.Second,
		leaseDuration:     30 * time.Second,
		jitter:            newDefaultJitter(clk),
		logger:            slog.Default(),
	}

	for _, o := range opts {
		o(e)
	}

	if err := e.validateIntervals(); err != nil {
		return nil, err
	}
	return e, nil
}

// validateIntervals checks the interval/lease constraint after options are applied.
func (e *Executor) validateIntervals() error {
	if e.heartbeatInterval <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga/executor: heartbeatInterval must be > 0",
		)
	}
	if e.leaseDuration <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga/executor: leaseDuration must be > 0",
		)
	}
	if e.heartbeatInterval*2 >= e.leaseDuration {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga/executor: heartbeatInterval*2 must be < leaseDuration",
		)
	}
	return nil
}

// Execute runs the given step against inst, retrying according to the effective
// retry policy (step.RetryPolicy → defPolicy → package defaults). Each attempt
// is guarded by per-step timeout (step.Timeout > 0) or the inherited ctx
// deadline (step.Timeout == 0). A heartbeat goroutine renews the lease while
// the step runs.
//
// leaseID is the fence token that identifies the current coordinator's claim;
// it is forwarded to every Heartbeat call.
//
// The returned Result carries Outcome, NewState (on success), Err (on failure),
// and Attempts (total invocations).
func (e *Executor) Execute(
	ctx context.Context,
	inst *ksaga.Instance,
	leaseID idutil.SafeID,
	step ksaga.Step,
	defPolicy ksaga.RetryPolicy,
	prevState []byte,
) Result {
	policy := resolvePolicy(step.RetryPolicy, defPolicy)

	for attempt := 1; ; attempt++ {
		result, done := e.runAttempt(ctx, inst, leaseID, step, policy, prevState, attempt)
		if done {
			return result
		}
		// Attempt failed and retry is allowed — compute and wait for backoff.
		delay := policy.Backoff(attempt-1, e.jitter)
		if sleepErr := e.clk.Sleep(ctx, e.clk.Now().Add(delay)); sleepErr != nil {
			return Result{Outcome: OutcomeExpired, Err: sleepErr, Attempts: attempt}
		}
	}
}

// runAttempt executes a single attempt of the step. Returns (result, true) when
// the loop should terminate (success, expiry, or retries exhausted); returns
// (zero, false) when the loop should retry.
func (e *Executor) runAttempt(
	ctx context.Context,
	inst *ksaga.Instance,
	leaseID idutil.SafeID,
	step ksaga.Step,
	policy resolvedPolicy,
	prevState []byte,
	attempt int,
) (Result, bool) {
	runCtx, cancelRun := e.buildRunCtx(ctx, step)
	defer cancelRun()

	// Start heartbeat goroutine.
	hbCtx, stopHB := context.WithCancel(runCtx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		runHeartbeat(hbCtx, e.clk, e.heartbeater,
			inst.ID, leaseID, e.heartbeatInterval, e.leaseDuration, e.logger, func() {})
	}()

	newState, runErr := safeRun(runCtx, step.Run, inst, prevState)

	// Always stop heartbeat and wait for it to exit before returning.
	stopHB()
	wg.Wait()

	if runCtx.Err() != nil {
		return Result{Outcome: OutcomeExpired, Err: runCtx.Err(), Attempts: attempt}, true
	}
	if runErr == nil {
		return Result{Outcome: OutcomeSucceeded, NewState: newState, Attempts: attempt}, true
	}
	if !policy.ShouldRetry(attempt) {
		outcome := OutcomeFailed
		if step.Compensate != nil {
			outcome = OutcomeCompensationRequired
		}
		return Result{Outcome: outcome, Err: runErr, Attempts: attempt}, true
	}
	return Result{}, false
}

// buildRunCtx derives the context for a single run attempt.
// If step.Timeout > 0, applies an absolute deadline; otherwise inherits ctx.
func (e *Executor) buildRunCtx(ctx context.Context, step ksaga.Step) (context.Context, context.CancelFunc) {
	if step.Timeout > 0 {
		deadline := e.clk.Now().Add(step.Timeout)
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithCancel(ctx)
}

// Compensate runs step.Compensate once (no retries, no step.Timeout).
// Returns nil when step.Compensate is nil (the step has nothing to undo).
// Panics inside the compensate function are recovered and returned as errors.
func (e *Executor) Compensate(
	ctx context.Context,
	inst *ksaga.Instance,
	step ksaga.Step,
	committedState []byte,
) error {
	if step.Compensate == nil {
		return nil
	}
	e.logger.InfoContext(ctx, "saga executor: compensating step",
		slog.String("instance_id", string(inst.ID)),
		slog.String("step_name", string(step.Name)),
	)
	if err := safeRunCompensate(ctx, step.Compensate, inst, committedState); err != nil {
		e.logger.WarnContext(ctx, "saga executor: compensate failed",
			slog.String("instance_id", string(inst.ID)),
			slog.String("step_name", string(step.Name)),
			slog.Any("error", err),
		)
		return err
	}
	return nil
}

// safeRun is the exclusive call site for ksaga.StepFunc invocations
// (SAGA-STEP-RUN-OUTSIDE-TX-01 A1). It wraps fn in a recover-based panic
// guard that converts any panic into an errcode.KindInternal error rather
// than crashing the process.
func safeRun(
	ctx context.Context,
	fn ksaga.StepFunc,
	inst *ksaga.Instance,
	prev []byte,
) (newState []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errcode.New(errcode.KindInternal, errcode.ErrInternal,
				"runtime/saga/executor: step Run panicked",
				errcode.WithInternal(fmt.Sprintf("panic: %v", r)),
			)
		}
	}()
	return fn(ctx, inst, prev)
}

// safeRunCompensate is the call site for ksaga.CompensateFunc invocations.
// Like safeRun it recovers from panics and converts them to errors.
func safeRunCompensate(
	ctx context.Context,
	fn ksaga.CompensateFunc,
	inst *ksaga.Instance,
	committedState []byte,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errcode.New(errcode.KindInternal, errcode.ErrInternal,
				"runtime/saga/executor: step Compensate panicked",
				errcode.WithInternal(fmt.Sprintf("panic: %v", r)),
			)
		}
	}()
	return fn(ctx, inst, committedState)
}
