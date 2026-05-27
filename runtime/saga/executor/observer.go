package executor

import "context"

// Observer receives best-effort events from Executor for observability sinks
// (metrics counters, tracing, audit). All methods MUST be non-blocking, must
// not panic, and must tolerate canceled contexts — they are called on hot
// paths (every Execute outcome / every retry attempt / every failed heartbeat
// tick) and must never affect Executor correctness.
//
// ref: temporalio/sdk-go internal_task_handlers.go — server-emitted activity
// metric envelope (best-effort, non-blocking sink).
type Observer interface {
	// ObserveOutcome is called exactly once per Execute call, at the terminal
	// Result. attempts is Result.Attempts (total invocations).
	ObserveOutcome(ctx context.Context, definitionID, stepName string, outcome Outcome, attempts int)

	// ObserveRetry is called between attempts, before the backoff Sleep starts.
	// Not called for the first attempt (only when attempt N > 1 is about to run).
	ObserveRetry(ctx context.Context, definitionID, stepName string)

	// ObserveHeartbeatFailure is called on each heartbeat tick that fails or
	// observes a stale lease. reason is one of HeartbeatFailureReason constants.
	ObserveHeartbeatFailure(ctx context.Context, reason HeartbeatFailureReason)
}

// HeartbeatFailureReason is a typed enum of why a heartbeat tick reported
// failure. The values are stable wire-facing strings used as metric labels
// (saga_heartbeat_failed_total{reason}) and slog fields.
type HeartbeatFailureReason string

const (
	// HeartbeatFailureInfraError means Heartbeater returned a non-nil err
	// (transient infra fault — e.g., DB timeout, network blip). The heartbeat
	// goroutine logs Warn and continues to the next tick (fail-open transient).
	HeartbeatFailureInfraError HeartbeatFailureReason = "infra_error"
	// HeartbeatFailureStaleLease means Heartbeater returned ok=false: another
	// coordinator has taken over the instance. The heartbeat goroutine logs
	// Warn, invokes the onStale callback (cancels the run context with
	// errLeaseLost), and returns.
	HeartbeatFailureStaleLease HeartbeatFailureReason = "stale_lease"
)

// NopObserver is the zero-cost default Observer. All methods are no-ops.
type NopObserver struct{}

// ObserveOutcome implements Observer.
func (NopObserver) ObserveOutcome(context.Context, string, string, Outcome, int) {}

// ObserveRetry implements Observer.
func (NopObserver) ObserveRetry(context.Context, string, string) {}

// ObserveHeartbeatFailure implements Observer.
func (NopObserver) ObserveHeartbeatFailure(context.Context, HeartbeatFailureReason) {}

// Compile-time interface satisfaction check.
var _ Observer = NopObserver{}
