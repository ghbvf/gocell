package executor

import (
	"context"

	"github.com/ghbvf/gocell/pkg/idutil"
)

// Observer receives best-effort events from Executor for observability sinks
// (metrics counters, tracing, audit). All methods MUST be non-blocking and
// must tolerate canceled contexts — they are called on hot paths (every
// Execute outcome / every retry attempt / every failed heartbeat tick) and
// must never affect Executor correctness.
//
// SHOULD NOT panic; the Executor wraps each Observer call in a defer/recover
// guard so a misbehaving observer logs Warn and execution continues —
// observability is best-effort and never affects step correctness.
// Recovery is provided by Executor.safeObserveOutcome / safeObserveRetry /
// safeObserveHeartbeatFailure helpers (executor.go).
//
// instanceID and leaseID are carried on every method for audit / tracing
// purposes (e.g. structured log correlation, future audit sink). Metric
// collectors SHOULD NOT use instanceID or leaseID as label dimensions — they
// are high-cardinality per-instance values that would cause cardinality
// explosion in time-series stores. Use definition_id / step_name / outcome /
// reason for metric labels and carry instanceID / leaseID only in log fields
// or trace span attributes.
//
// ref: temporalio/sdk-go internal_task_handlers.go — server-emitted activity
// metric envelope (best-effort, non-blocking sink).
type Observer interface {
	// ObserveOutcome is called exactly once per Execute call, at the terminal
	// Result. attempts is Result.Attempts (total invocations).
	// instanceID and leaseID identify the specific instance execution for
	// audit / tracing — see Observer godoc for cardinality guidance.
	ObserveOutcome(
		ctx context.Context,
		instanceID, leaseID idutil.SafeID,
		definitionID, stepName string,
		outcome Outcome,
		attempts int,
	)

	// ObserveRetry is called between attempts, before the backoff Sleep starts.
	// Not called for the first attempt (only when attempt N > 1 is about to run).
	// instanceID and leaseID identify the specific instance execution for
	// audit / tracing — see Observer godoc for cardinality guidance.
	ObserveRetry(
		ctx context.Context,
		instanceID, leaseID idutil.SafeID,
		definitionID, stepName string,
	)

	// ObserveHeartbeatFailure is called on each heartbeat tick that fails or
	// observes a stale lease. reason is one of HeartbeatFailureReason constants.
	// instanceID and leaseID identify the specific instance execution for
	// audit / tracing — see Observer godoc for cardinality guidance.
	ObserveHeartbeatFailure(ctx context.Context, instanceID idutil.SafeID, leaseID idutil.SafeID, reason HeartbeatFailureReason)
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
func (NopObserver) ObserveOutcome(_ context.Context, _ idutil.SafeID, _ idutil.SafeID, _, _ string, _ Outcome, _ int) {
}

// ObserveRetry implements Observer.
func (NopObserver) ObserveRetry(_ context.Context, _ idutil.SafeID, _ idutil.SafeID, _, _ string) {}

// ObserveHeartbeatFailure implements Observer.
func (NopObserver) ObserveHeartbeatFailure(_ context.Context, _ idutil.SafeID, _ idutil.SafeID, _ HeartbeatFailureReason) {
}

// Compile-time interface satisfaction check.
var _ Observer = NopObserver{}
