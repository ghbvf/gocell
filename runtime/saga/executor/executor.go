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
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Outcome classifies the result of an Execute call.
// The zero value is intentionally invalid — callers must check for known
// Outcome constants, not treat zero as a sentinel.
type Outcome uint8

const (
	// OutcomeSucceeded means the step's Run returned a nil error.
	OutcomeSucceeded Outcome = iota + 1
	// OutcomeFailed means retry attempts were exhausted with a non-nil error.
	// This is an execution FACT only — the executor does NOT decide whether to
	// compensate. The Coordinator chooses Compensating vs Failed from committed
	// step history (kernel/saga/status.go).
	OutcomeFailed
	// OutcomeExpired means a deadline elapsed: either the per-step Step.Timeout
	// or a parent (saga-level Definition.Timeout) deadline. Terminal.
	OutcomeExpired
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
	case OutcomeCanceled:
		return "Canceled"
	case OutcomeLeaseLost:
		return "LeaseLost"
	default:
		return "Outcome(" + strconv.Itoa(int(o)) + ")"
	}
}

// Executor default lease parameters and the internal cancel-cause sentinels.
const (
	// DefaultHeartbeatInterval is the lease renewal cadence when
	// WithHeartbeatInterval is unset.
	DefaultHeartbeatInterval = 10 * time.Second
	// DefaultLeaseDuration is the lease TTL passed to Heartbeat when
	// WithLeaseDuration is unset.
	DefaultLeaseDuration = 30 * time.Second
	// HeartbeatLeaseSafetyFactor is the minimum factor by which LeaseDuration
	// must exceed HeartbeatInterval so at least one heartbeat fires before
	// lease expiry: heartbeatInterval * HeartbeatLeaseSafetyFactor < leaseDuration.
	// Exported so the Coordinator can reference the same constant without
	// duplicating the value (cross-package single source of truth, #1210 C9).
	HeartbeatLeaseSafetyFactor = 2
)

// Internal cancel-cause sentinels distinguish why the run context ended. They
// are unexported (callers switch on Outcome, not on these errors); see
// classifyCanceled for the cause→Outcome mapping.
var (
	errLeaseLost   = errors.New("saga executor: lease lost")
	errStepTimeout = errors.New("saga executor: step timeout elapsed")
)

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
	observer          Observer
	tracer            wrapper.Tracer
}

// safeObserveOutcome / safeObserveRetry / safeObserveHeartbeatFailure wrap
// every Observer call (#1181 F7). The Observer contract documents that
// implementations MUST NOT panic, but it is a Soft contract — a faulty metrics
// adapter that does panic would otherwise crash the executor goroutine and
// drop the instance lease. defer recover() keeps observability strictly
// best-effort: a misbehaving observer logs Warn (with redacted panic payload
// per F10) and execution continues.
func (e *Executor) safeObserveOutcome(ctx context.Context, defID, stepName string, outcome Outcome, attempts int) {
	defer e.recoverObserverPanic(ctx, "ObserveOutcome")
	e.observer.ObserveOutcome(ctx, defID, stepName, outcome, attempts)
}

func (e *Executor) safeObserveRetry(ctx context.Context, defID, stepName string) {
	defer e.recoverObserverPanic(ctx, "ObserveRetry")
	e.observer.ObserveRetry(ctx, defID, stepName)
}

func (e *Executor) safeObserveHeartbeatFailure(ctx context.Context, reason HeartbeatFailureReason) {
	defer e.recoverObserverPanic(ctx, "ObserveHeartbeatFailure")
	e.observer.ObserveHeartbeatFailure(ctx, reason)
}

// recoverObserverPanic is the shared recover handler for observer calls.
// The panic payload is redacted through pkg/redaction.RedactString before
// reaching slog so a panic value carrying user data (sensitive headers, JWT
// fragments) does not leak into operator logs (#1181 F10, mirrors
// .claude/rules/gocell/observability.md §Span Error Redaction).
func (e *Executor) recoverObserverPanic(ctx context.Context, method string) {
	if r := recover(); r != nil {
		e.logger.WarnContext(ctx, "saga executor: observer call panicked, ignoring",
			slog.String("method", method),
			slog.Any("panic", redaction.RedactAny(r)),
		)
	}
}

// IsLeaseLost reports whether err signals that the instance lease was lost
// during a RunWithHeartbeat invocation (another coordinator took over).
// Callers driving compensation walks use this to distinguish "another leader
// took over — stop quietly" from real fn errors.
//
// Implementation note: uses errors.Is on an unexported sentinel
// (errLeaseLost). Only errors returned from RunWithHeartbeat can satisfy
// IsLeaseLost. Execute does NOT route lease-loss through error — it returns
// Result{Outcome: OutcomeLeaseLost}; IsLeaseLost is therefore not
// applicable to Result.Err. Issue #1181 design decision: lease-loss
// proactively cancels the running step via context cancelCause(errLeaseLost)
// — step authors must select on ctx.Done() to honor the cancellation.
func IsLeaseLost(err error) bool {
	return errors.Is(err, errLeaseLost)
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
		heartbeatInterval: DefaultHeartbeatInterval,
		leaseDuration:     DefaultLeaseDuration,
		jitter:            newDefaultJitter(clk),
		logger:            slog.Default(),
		observer:          NopObserver{},
		tracer:            wrapper.NoopTracer{},
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
	if e.heartbeatInterval*HeartbeatLeaseSafetyFactor >= e.leaseDuration {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"runtime/saga/executor: heartbeatInterval*2 must be < leaseDuration",
		)
	}
	return nil
}

// Execute runs the given step against inst, retrying according to the effective
// retry policy (step.RetryPolicy → defPolicy → package defaults). Each attempt
// is guarded by per-step timeout (step.Timeout > 0) or the inherited ctx
// deadline (step.Timeout == 0).
//
// A single heartbeat goroutine spans the WHOLE Execute call — the step run AND
// every retry backoff — so the lease cannot be dropped during a long backoff
// (C1/F5). If a heartbeat observes a stale lease (ok=false), it cancels runCtx
// with errLeaseLost, which both unblocks the running step / backoff and routes
// the result to OutcomeLeaseLost (C1/F4).
//
// runCtx is cancelable-with-cause so the terminal Outcome distinguishes the four
// end reasons (see classifyCanceled): lease lost, parent shutdown cancel, parent
// (saga-level) deadline, and per-step timeout.
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
	defID := string(inst.DefinitionID)
	stepName := string(step.Name)
	// Per-step trace span. Attrs at start carry the static identity; the final
	// attempt count + outcome are set after executeInner returns so a single
	// span reflects the complete retry history. Tracer defaults to NoopTracer.
	ctx, span := e.tracer.Start(ctx, "saga.executor.step.run",
		wrapper.Attr{Key: "saga.instance_id", Value: string(inst.ID)},
		wrapper.Attr{Key: "saga.definition_id", Value: defID},
		wrapper.Attr{Key: "saga.step_name", Value: stepName},
	)
	defer span.End()

	result := e.executeInner(ctx, inst, leaseID, step, defPolicy, prevState)

	span.SetAttributes(
		wrapper.Attr{Key: "saga.attempts", Value: int64(result.Attempts)},
		wrapper.Attr{Key: "saga.outcome", Value: result.Outcome.String()},
	)
	if result.Outcome != OutcomeSucceeded {
		if result.Err != nil {
			// Redact before recording — span sinks are operator-visible and
			// must not leak secrets that could appear in step.Run errors
			// (per .claude/rules/gocell/observability.md §Span Error Redaction).
			span.RecordError(redaction.RedactError(result.Err))
		}
		span.SetStatus(wrapper.StatusError, result.Outcome.String())
	}

	e.safeObserveOutcome(ctx, defID, stepName, result.Outcome, result.Attempts)
	return result
}

// executeInner is the retry-and-heartbeat loop body. Separated from Execute so
// the terminal observer.ObserveOutcome call lives at exactly one site
// regardless of which inner branch produced the Result.
func (e *Executor) executeInner(
	ctx context.Context,
	inst *ksaga.Instance,
	leaseID idutil.SafeID,
	step ksaga.Step,
	defPolicy ksaga.RetryPolicy,
	prevState []byte,
) Result {
	// Preflight heartbeat (#1181 F6) — synchronously confirm the lease is
	// still ours BEFORE running step side-effects or starting the heartbeat
	// goroutine. Without this, a stale lease is detected only at the first
	// async tick (heartbeatInterval ≥ 10s by default) — long enough for a
	// non-idempotent forward step to externalize side effects under a lease
	// another coordinator already owns. Infra errors here fail-open (treat
	// as if heartbeat succeeded) since the async heartbeat goroutine will
	// detect persistent infra failure via the regular tick budget.
	if ok, err := e.heartbeater.Heartbeat(ctx, inst.ID, leaseID, e.leaseDuration); err == nil && !ok {
		e.logger.InfoContext(ctx, "saga executor: preflight heartbeat reported stale lease",
			slog.String("instance_id", string(inst.ID)),
			slog.String("lease_id", string(leaseID)),
		)
		e.safeObserveHeartbeatFailure(ctx, HeartbeatFailureStaleLease)
		return Result{Outcome: OutcomeLeaseLost, Err: errLeaseLost, Attempts: 0}
	}

	policy := resolvePolicy(step.RetryPolicy, defPolicy)
	defID := string(inst.DefinitionID)
	stepName := string(step.Name)

	// runCtx governs both the step run and the backoff waits, for the whole call.
	runCtx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)

	// hbCtx is the heartbeat-only lifetime. Stopping it on normal completion is
	// NOT a lease-lost signal — only onStale sets a cause on runCtx, so
	// classification never confuses "heartbeat goroutine exited" with "stale".
	hbCtx, stopHB := context.WithCancel(runCtx)
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	// onStale cancels runCtx with errLeaseLost. cancelCause is first-cause-wins
	// idempotent, and the heartbeat goroutine returns after the first ok=false,
	// so no sync.Once is needed.
	onStale := func() { cancelCause(errLeaseLost) }
	onHBFailure := func(reason HeartbeatFailureReason) {
		e.safeObserveHeartbeatFailure(runCtx, reason)
	}
	go func() {
		defer hbWG.Done()
		defer func() {
			if r := recover(); r != nil {
				// #1181 F10: redact panic payload — slog sinks are operator-
				// visible and the panic value may carry headers / tokens.
				e.logger.WarnContext(hbCtx, "saga executor: heartbeat goroutine recovered from panic",
					slog.String("instance_id", string(inst.ID)),
					slog.Any("panic", redaction.RedactAny(r)),
				)
				onStale() // cancel runCtx with errLeaseLost so the in-flight step bails
			}
		}()
		runHeartbeat(hbCtx, e.clk, e.heartbeater,
			inst.ID, leaseID, inst.DefinitionID,
			e.heartbeatInterval, e.leaseDuration,
			e.logger, onStale, onHBFailure)
	}()
	stopAndJoin := func() { stopHB(); hbWG.Wait() }

	for attempt := 1; ; attempt++ {
		result, done := e.runAttempt(runCtx, inst, step, policy, prevState, attempt)
		if done {
			stopAndJoin()
			return result
		}
		// Attempt failed and retry is allowed — observer fires BEFORE the
		// backoff Sleep so a stuck heartbeater that races to cancel runCtx
		// during Sleep does not swallow the retry signal.
		e.safeObserveRetry(runCtx, defID, stepName)
		// Wait for backoff while the heartbeat keeps renewing the lease. A
		// runCtx cancellation (parent shutdown/deadline or lease lost) unblocks
		// the Sleep immediately.
		delay := policy.Backoff(attempt-1, e.jitter)
		if sleepErr := e.clk.Sleep(runCtx, e.clk.Now().Add(delay)); sleepErr != nil {
			stopAndJoin()
			return e.classifyCanceled(runCtx, sleepErr, attempt)
		}
	}
}

// RunWithHeartbeat invokes fn under an active heartbeat goroutine that renews
// the instance lease via the executor's Heartbeater for the lifetime of fn.
// The heartbeat goroutine is created and joined inside this call.
//
// If a heartbeat tick observes a stale lease (ok=false), fn's ctx is canceled
// with the lease-lost sentinel and RunWithHeartbeat returns an error satisfying
// IsLeaseLost. If fn returns first, the heartbeat is stopped and fn's error is
// returned unchanged (lease-loss never overrides a real fn error).
//
// Used by Coordinator.runCompensation to keep the lease alive during a long
// reverse-compensation walk. Internally, Execute uses runHeartbeat directly
// because its retry loop needs more nuanced control over the goroutine — both
// paths share runHeartbeat as the heartbeat-goroutine body.
//
// Return-value precedence (correctness invariant):
//   - If fn returns a real domain error (not nil / context.Canceled /
//     context.DeadlineExceeded), it is returned as-is. Lease-loss never
//     overrides a real fn error — domain failures must surface.
//   - If fn returns nil OR a ctx-derived error (context.Canceled /
//     context.DeadlineExceeded) AND a heartbeat concurrently observed a
//     stale lease, the lease-lost sentinel overrides the return.
//     Callers detect this via IsLeaseLost.
//   - Otherwise fn's return is propagated unchanged.
//
// ref: temporalio/sdk-go internal_task_handlers.go (per-activity heartbeat
// goroutine spanning the whole activity invocation).
func (e *Executor) RunWithHeartbeat(
	ctx context.Context,
	inst *ksaga.Instance,
	leaseID idutil.SafeID,
	fn func(ctx context.Context) error,
) error {
	runCtx, cancelCause := context.WithCancelCause(ctx)
	defer cancelCause(nil)

	hbCtx, stopHB := context.WithCancel(runCtx)
	var hbWG sync.WaitGroup
	hbWG.Add(1)
	onStale := func() { cancelCause(errLeaseLost) }
	onHBFailure := func(reason HeartbeatFailureReason) {
		e.safeObserveHeartbeatFailure(runCtx, reason)
	}
	go func() {
		defer hbWG.Done()
		defer func() {
			if r := recover(); r != nil {
				e.logger.WarnContext(hbCtx, "saga executor: heartbeat goroutine recovered from panic",
					slog.String("instance_id", string(inst.ID)),
					slog.Any("panic", redaction.RedactAny(r)),
				)
				onStale() // cancel runCtx with errLeaseLost so the in-flight fn bails
			}
		}()
		runHeartbeat(hbCtx, e.clk, e.heartbeater,
			inst.ID, leaseID, inst.DefinitionID,
			e.heartbeatInterval, e.leaseDuration,
			e.logger, onStale, onHBFailure)
	}()
	defer func() {
		stopHB()
		hbWG.Wait()
	}()

	fnErr := fn(runCtx)
	// Lease-lost only overrides a nil/ctx-derived fn return — a real domain
	// error from fn is reported as-is. errors.Is on cause survives ctx
	// propagation; classifyCanceled in Execute uses the same precedence.
	if fnErr == nil || errors.Is(fnErr, context.Canceled) || errors.Is(fnErr, context.DeadlineExceeded) {
		if cause := context.Cause(runCtx); errors.Is(cause, errLeaseLost) {
			return errLeaseLost
		}
	}
	return fnErr
}

// runAttempt executes a single attempt of the step. Returns (result, true) when
// the loop should terminate (success, expiry, cancel, lease lost, or retries
// exhausted); returns (zero, false) when the loop should retry.
//
// Cause precedence is a correctness invariant: stepCtx is a child of runCtx, so
// a parent/lease cancellation propagates down and fires stepCtx too. We MUST
// check context.Cause(runCtx) BEFORE the per-step timeout, otherwise a lease
// loss or parent cancel could be misreported as a step-timeout Expired.
func (e *Executor) runAttempt(
	runCtx context.Context,
	inst *ksaga.Instance,
	step ksaga.Step,
	policy resolvedPolicy,
	prevState []byte,
	attempt int,
) (Result, bool) {
	stepCtx, cancelStep := e.buildStepCtx(runCtx, step)
	defer cancelStep(nil)

	newState, runErr := safeRun(stepCtx, step.Run, inst, prevState)

	// 1) Parent shutdown/deadline or lease lost wins over everything.
	if cause := context.Cause(runCtx); cause != nil {
		return e.classifyCanceled(runCtx, cause, attempt), true
	}
	// 2) Per-step timeout (clock-driven via buildStepCtx's AfterFunc).
	if errors.Is(context.Cause(stepCtx), errStepTimeout) {
		return Result{Outcome: OutcomeExpired, Err: errStepTimeout, Attempts: attempt}, true
	}
	// 3) Success.
	if runErr == nil {
		return Result{Outcome: OutcomeSucceeded, NewState: newState, Attempts: attempt}, true
	}
	// 4) Retries exhausted — report the FACT only (the Coordinator decides
	//    Compensating vs Failed from committed history; the executor must not
	//    inspect step.Compensate here).
	if !policy.ShouldRetry(attempt) {
		e.logger.WarnContext(runCtx, "saga executor: retry budget exhausted",
			slog.String("instance_id", string(inst.ID)),
			slog.String("step_name", string(step.Name)),
			slog.Int("attempts", attempt),
			slog.String("outcome", OutcomeFailed.String()),
			slog.Any("error", runErr),
		)
		return Result{Outcome: OutcomeFailed, Err: runErr, Attempts: attempt}, true
	}
	return Result{}, false
}

// classifyCanceled maps a runCtx cancellation cause to the terminal Outcome.
// The four causes are mutually exclusive at any given cancellation:
//   - errLeaseLost           → OutcomeLeaseLost (another coordinator owns the lease)
//   - context.DeadlineExceeded → OutcomeExpired  (saga-level Definition.Timeout)
//   - context.Canceled (default) → OutcomeCanceled (explicit shutdown/abort)
//
// observedErr is the error actually seen by the caller (ctx.Err() / Sleep err);
// it is carried in Result.Err for logging.
func (e *Executor) classifyCanceled(runCtx context.Context, observedErr error, attempt int) Result {
	cause := context.Cause(runCtx)
	switch {
	case errors.Is(cause, errLeaseLost):
		return Result{Outcome: OutcomeLeaseLost, Err: errLeaseLost, Attempts: attempt}
	case errors.Is(cause, context.DeadlineExceeded):
		return Result{Outcome: OutcomeExpired, Err: observedErr, Attempts: attempt}
	default:
		return Result{Outcome: OutcomeCanceled, Err: observedErr, Attempts: attempt}
	}
}

// buildStepCtx derives the per-attempt context. When step.Timeout > 0, a
// clock-driven AfterFunc cancels the context with errStepTimeout — this is what
// makes a FakeClock.Advance deterministically trigger the timeout (no real
// wall-clock dependency, fixing F3). When step.Timeout == 0, the step inherits
// runCtx directly. The returned cancel func stops the timer (avoiding a pending
// timer leak) and is safe to call with nil for deferred cleanup, since the first
// cause set wins.
func (e *Executor) buildStepCtx(runCtx context.Context, step ksaga.Step) (context.Context, context.CancelCauseFunc) {
	if step.Timeout <= 0 {
		return context.WithCancelCause(runCtx)
	}
	stepCtx, cancel := context.WithCancelCause(runCtx)
	timer := e.clk.AfterFunc(e.clk.Now().Add(step.Timeout), func() {
		cancel(errStepTimeout)
	})
	return stepCtx, func(cause error) {
		timer.Stop()
		cancel(cause)
	}
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
	// Per-step compensate trace span. Tracer defaults to NoopTracer when no
	// adapter is wired, so this is zero-allocation in tests.
	ctx, span := e.tracer.Start(ctx, "saga.executor.step.compensate",
		wrapper.Attr{Key: "saga.instance_id", Value: string(inst.ID)},
		wrapper.Attr{Key: "saga.definition_id", Value: string(inst.DefinitionID)},
		wrapper.Attr{Key: "saga.step_name", Value: string(step.Name)},
	)
	defer span.End()

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
		// Redact before recording — see ObserveOutcome rationale above.
		span.RecordError(redaction.RedactError(err))
		span.SetStatus(wrapper.StatusError, "compensate failed")
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
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("panic: %v", r))),
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
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("panic: %v", r))),
			)
		}
	}()
	return fn(ctx, inst, committedState)
}
