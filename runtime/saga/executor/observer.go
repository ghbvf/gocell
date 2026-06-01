package executor

import (
	"context"

	"github.com/ghbvf/gocell/pkg/idutil"
)

// Observer is the saga-wide best-effort observability sink (metrics counters,
// tracing, audit). It carries two families of events:
//
//   - Executor-emitted (per-step hot path): ObserveOutcome / ObserveRetry /
//     ObserveHeartbeatFailure — called by the Executor on every Execute
//     outcome / retry attempt / failed heartbeat tick.
//   - Coordinator-emitted (per-tick / per-drive / per-skip): ObserveTick /
//     ObserveDrive / ObserveLeaderSkip — called by runtime/saga.Coordinator on
//     each ClaimPending cycle, each driveOne, and each leader-elect skip. The
//     contract (interface) lives here for cohesion: the Executor is the lower
//     layer the Coordinator imports, so a single saga observability sink can
//     satisfy both producers (the Coordinator forwards this same value into the
//     internally-constructed Executor via WithObserver).
//
// All methods MUST be non-blocking and must tolerate canceled contexts — they
// are called on hot paths and must never affect saga correctness.
//
// SHOULD NOT panic; both producers wrap each Observer call in a defer/recover
// guard so a misbehaving observer logs Warn and execution continues —
// observability is best-effort and never affects correctness. Recovery is
// provided by Executor.safeObserveOutcome / safeObserveRetry /
// safeObserveHeartbeatFailure helpers (executor.go) and Coordinator.safeObserve
// (coordinator.go).
//
// instanceID and leaseID are carried on the Executor-emitted methods for audit
// / tracing purposes (e.g. structured log correlation, future audit sink).
// Metric collectors SHOULD NOT use instanceID or leaseID as label dimensions —
// they are high-cardinality per-instance values that would cause cardinality
// explosion in time-series stores. Use definition_id / step_name / outcome /
// reason / result for metric labels and carry instanceID / leaseID only in log
// fields or trace span attributes.
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

	// ObserveTick is called once per Coordinator ClaimPending cycle that runs
	// (the stopping-drain early return is not counted). result classifies the
	// cycle: claimed (≥1 instance), empty (0 claimed), or error (ClaimPending
	// failed). Coordinator-emitted — definition_id is not available pre-claim.
	ObserveTick(ctx context.Context, result TickResult)

	// ObserveDrive is called once per driveOne completion with the per-instance
	// outcome: ok (driveOne returned nil) or error (non-nil). Coordinator-emitted;
	// definition_id is the bounded label dimension (per-instance IDs excluded).
	ObserveDrive(ctx context.Context, definitionID string, result DriveResult)

	// ObserveLeaderSkip is called each time the leader-elect gate skips an
	// instance this tick (acquireLead could not confirm leadership). reason is
	// one of LeaderSkipReason constants. Coordinator-emitted; backend_error is
	// the distlock lock-acquire failure rate, contended is normal multi-process
	// contention. definition_id is the bounded label dimension.
	ObserveLeaderSkip(ctx context.Context, definitionID string, reason LeaderSkipReason)
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

// TickResult is a typed enum classifying a Coordinator ClaimPending cycle. The
// values are stable wire-facing strings used as the saga_tick_total{result}
// metric label. Value set frozen by SAGA-METRIC-LABEL-VALUES-FROZEN-01.
type TickResult string

const (
	// TickClaimed means ClaimPending returned ≥1 instance this cycle.
	TickClaimed TickResult = "claimed"
	// TickEmpty means ClaimPending returned 0 instances (idle tick).
	TickEmpty TickResult = "empty"
	// TickError means ClaimPending itself failed (journal/backend fault).
	TickError TickResult = "error"
)

// DriveResult is a typed enum classifying a single driveOne completion. The
// values are stable wire-facing strings used as the saga_drive_total{result}
// metric label. Value set frozen by SAGA-METRIC-LABEL-VALUES-FROZEN-01.
type DriveResult string

const (
	// DriveOK means driveOne returned nil (instance advanced cleanly).
	DriveOK DriveResult = "ok"
	// DriveError means driveOne returned a non-nil error (stale lease, instance
	// gone, step failure surfaced to the coordinator, etc.).
	DriveError DriveResult = "error"
)

// LeaderSkipReason is a typed enum of why the leader-elect gate skipped an
// instance (acquireLead returned lead=false). The values are stable wire-facing
// strings used as the saga_leader_elect_skip_total{reason} metric label and as
// slog fields. Value set frozen by SAGA-METRIC-LABEL-VALUES-FROZEN-01.
type LeaderSkipReason string

const (
	// LeaderSkipContended means another coordinator holds the per-instance
	// distlock (ErrDistlockTimeout) — expected in multi-process deployments.
	LeaderSkipContended LeaderSkipReason = "contended"
	// LeaderSkipCtxCanceled means the acquire was aborted by ctx cancellation or
	// deadline (normal Stop()/shutdown of the tick loop).
	LeaderSkipCtxCanceled LeaderSkipReason = "ctx_canceled"
	// LeaderSkipBackendError means a distlock backend I/O fault prevented the
	// acquire — this is the lock-acquire failure rate (operationally interesting).
	LeaderSkipBackendError LeaderSkipReason = "backend_error"
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

// ObserveTick implements Observer.
func (NopObserver) ObserveTick(_ context.Context, _ TickResult) {}

// ObserveDrive implements Observer.
func (NopObserver) ObserveDrive(_ context.Context, _ string, _ DriveResult) {}

// ObserveLeaderSkip implements Observer.
func (NopObserver) ObserveLeaderSkip(_ context.Context, _ string, _ LeaderSkipReason) {}

// Compile-time interface satisfaction check.
var _ Observer = NopObserver{}
