package metrics

import (
	"context"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/runtime/saga/executor"
)

// SagaCollector registers the six saga metrics scoped to the owner cell
// supplied at construction. One collector per Coordinator (= per-cell). It
// satisfies executor.Observer, the saga-wide observability sink, so the same
// collector receives both Executor-emitted (step-level) and Coordinator-emitted
// (tick / drive / leader-skip) events.
//
// Step-level metrics (Executor-emitted):
//   - saga_step_outcome_total{cell,definition_id,outcome}: total Execute calls
//     terminated, labeled by the 5 Outcome variants
//     (succeeded / failed / expired / canceled / lease_lost). step_name is
//     intentionally NOT a label to keep the {step × outcome} Cartesian product
//     bounded — outcome-level dashboards are aggregated across steps.
//   - saga_step_retry_total{cell,definition_id,step_name}: total retry
//     attempts emitted between Execute runAttempt invocations (attempts ≥ 2).
//     step_name IS a label here because retry-budget tuning is per-step.
//   - saga_heartbeat_failed_total{cell,reason}: total heartbeat tick failures,
//     labeled by reason (infra_error / stale_lease). definition_id and
//     step_name are excluded — heartbeat failure is an infra-level signal,
//     not a per-step business metric.
//
// Coordinator-level metrics (Coordinator-emitted):
//   - saga_tick_total{cell,result}: total ClaimPending cycles, labeled by
//     result (claimed / empty / error). Loop liveness + claim activity;
//     definition_id is not available pre-claim.
//   - saga_drive_total{cell,definition_id,result}: total driveOne completions,
//     labeled by result (ok / error). Per-instance forward-progress throughput.
//   - saga_leader_elect_skip_total{cell,definition_id,reason}: total
//     leader-elect skips, labeled by reason (contended / ctx_canceled /
//     backend_error). reason="contended" sustained with drive{result="ok"}≈0
//     signals an instance stuck skipping; reason="backend_error" is the
//     distlock lock-acquire failure rate. Replaces log-scraping the Debug-level
//     skip path (#1109).
//
// Cardinality discipline: definition_id × step_name is the worst case; all
// label dimensions are static enumeration sets (registered at compile time).
// result/reason add ≤3 values each over a bounded definition_id set. Expected
// upper bound: ≤ 300 active series per cell; metrics provider cap=2000 is the
// runtime tripwire.
//
// ref: temporalio/sdk-go internal_task_handlers.go — server-emitted
// activity outcome / heartbeat-failure metric envelope.
// ref: itimofeev/go-saga (no native metrics — pattern adapted from outbox).
type SagaCollector struct {
	cellID string

	outcome    kernelmetrics.CounterVec // saga_step_outcome_total{cell,definition_id,outcome}
	retry      kernelmetrics.CounterVec // saga_step_retry_total{cell,definition_id,step_name}
	hbFail     kernelmetrics.CounterVec // saga_heartbeat_failed_total{cell,reason}
	tick       kernelmetrics.CounterVec // saga_tick_total{cell,result}
	drive      kernelmetrics.CounterVec // saga_drive_total{cell,definition_id,result}
	leaderSkip kernelmetrics.CounterVec // saga_leader_elect_skip_total{cell,definition_id,reason}
}

// compile-time interface check.
var _ executor.Observer = (*SagaCollector)(nil)

// NewSagaCollector registers the six saga counters on the given provider.
// cellID is the owner cell of the Coordinator wiring this collector; empty is
// an error (no fallback to _runtime sentinel — the saga Coordinator is always
// owned by exactly one cell).
//
// Failure modes:
//   - p == nil → errcode.KindInvalid + ErrObservabilityConfigInvalid
//   - cellID == "" → errcode.KindInvalid + ErrObservabilityConfigInvalid
//   - any CounterVec registration error is wrapped with the metric name and
//     rolls back the counters registered earlier in the sequence (atomic).
//
// Caller contract: never pass a nil *SagaCollector — use NopObserver via
// WithObserver(nil) for explicit disable.
func NewSagaCollector(p kernelmetrics.Provider, cellID string) (*SagaCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SagaCollector Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SagaCollector cellID is required")
	}

	var registered []kernelmetrics.Collector
	c := &SagaCollector{cellID: cellID}
	var err error
	if c.outcome, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_step_outcome_total",
		Help: "Total saga step Execute outcomes (succeeded/failed/expired/canceled/lease_lost). " +
			"step_name is intentionally excluded to bound the step×outcome cardinality; " +
			"per-step drill-down uses saga_step_retry_total.",
		LabelNames: []string{"cell", "definition_id", "outcome"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.retry, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name:       "saga_step_retry_total",
		Help:       "Total retry attempts emitted by the saga executor (attempt N>1 fired) per definition+step.",
		LabelNames: []string{"cell", "definition_id", "step_name"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.hbFail, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_heartbeat_failed_total",
		Help: "Total heartbeat tick failures observed by the saga executor, labeled by reason " +
			"(infra_error = transient err; stale_lease = ok=false / another coordinator took over). " +
			"Excludes definition_id and step_name — heartbeat is an infra signal, not per-step.",
		LabelNames: []string{"cell", "reason"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.tick, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_tick_total",
		Help: "Total Coordinator ClaimPending cycles, labeled by result " +
			"(claimed = ≥1 instance; empty = idle tick; error = ClaimPending failed). " +
			"Loop liveness; definition_id is not available pre-claim.",
		LabelNames: []string{"cell", "result"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.drive, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_drive_total",
		Help: "Total Coordinator driveOne completions, labeled by result " +
			"(ok = advanced cleanly; error = stale lease / instance gone / step failure). " +
			"Per-instance forward-progress throughput.",
		LabelNames: []string{"cell", "definition_id", "result"},
	}, &registered); err != nil {
		return nil, err
	}
	if c.leaderSkip, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_leader_elect_skip_total",
		Help: "Total leader-elect skips (acquireLead could not confirm leadership), labeled by reason " +
			"(contended = another coordinator holds the distlock; ctx_canceled = shutdown; " +
			"backend_error = distlock I/O fault / lock-acquire failure rate). " +
			"Sustained contended with drive{result=ok}≈0 means an instance is stuck skipping.",
		LabelNames: []string{"cell", "definition_id", "reason"},
	}, &registered); err != nil {
		return nil, err
	}
	return c, nil
}

// ObserveOutcome implements executor.Observer.
// Records one increment on saga_step_outcome_total{cell,definition_id,outcome}.
// instanceID and leaseID are carried for future audit/tracing use; they are
// intentionally NOT used as metric label dimensions to prevent cardinality
// explosion (per-instance high-cardinality values — see Observer godoc).
// attempts is also not labeled; it is carried into trace spans and slog by
// the executor itself.
func (c *SagaCollector) ObserveOutcome(
	ctx context.Context,
	_ /* instanceID */ idutil.SafeID,
	_ /* leaseID */ idutil.SafeID,
	definitionID, _ /* stepName */ string,
	outcome executor.Outcome,
	_ /* attempts */ int,
) {
	c.outcome.With(kernelmetrics.Labels{
		"cell":          c.cellID,
		"definition_id": definitionID,
		"outcome":       outcomeLabel(outcome),
	}).Inc(ctx)
}

// ObserveRetry implements executor.Observer.
// Records one increment on saga_step_retry_total{cell,definition_id,step_name}.
// instanceID and leaseID are carried for future audit/tracing use; they are
// intentionally NOT used as metric label dimensions (see ObserveOutcome).
func (c *SagaCollector) ObserveRetry(
	ctx context.Context,
	_ /* instanceID */ idutil.SafeID,
	_ /* leaseID */ idutil.SafeID,
	definitionID, stepName string,
) {
	c.retry.With(kernelmetrics.Labels{
		"cell":          c.cellID,
		"definition_id": definitionID,
		"step_name":     stepName,
	}).Inc(ctx)
}

// ObserveHeartbeatFailure implements executor.Observer.
// Records one increment on saga_heartbeat_failed_total{cell,reason}.
// instanceID and leaseID are carried for future audit/tracing use; they are
// intentionally NOT used as metric label dimensions (see ObserveOutcome).
func (c *SagaCollector) ObserveHeartbeatFailure(
	ctx context.Context,
	_ /* instanceID */ idutil.SafeID,
	_ /* leaseID */ idutil.SafeID,
	reason executor.HeartbeatFailureReason,
) {
	c.hbFail.With(kernelmetrics.Labels{
		"cell":   c.cellID,
		"reason": string(reason),
	}).Inc(ctx)
}

// ObserveTick implements executor.Observer (Coordinator-emitted).
// Records one increment on saga_tick_total{cell,result}.
func (c *SagaCollector) ObserveTick(ctx context.Context, result executor.TickResult) {
	c.tick.With(kernelmetrics.Labels{
		"cell":   c.cellID,
		"result": string(result),
	}).Inc(ctx)
}

// ObserveDrive implements executor.Observer (Coordinator-emitted).
// Records one increment on saga_drive_total{cell,definition_id,result}.
func (c *SagaCollector) ObserveDrive(ctx context.Context, definitionID string, result executor.DriveResult) {
	c.drive.With(kernelmetrics.Labels{
		"cell":          c.cellID,
		"definition_id": definitionID,
		"result":        string(result),
	}).Inc(ctx)
}

// ObserveLeaderSkip implements executor.Observer (Coordinator-emitted).
// Records one increment on saga_leader_elect_skip_total{cell,definition_id,reason}.
func (c *SagaCollector) ObserveLeaderSkip(ctx context.Context, definitionID string, reason executor.LeaderSkipReason) {
	c.leaderSkip.With(kernelmetrics.Labels{
		"cell":          c.cellID,
		"definition_id": definitionID,
		"reason":        string(reason),
	}).Inc(ctx)
}

// outcomeLabel maps an Outcome to its wire-stable lowercase label value.
// Using fmt.Stringer would emit "Succeeded" (camel); metric labels follow
// the snake_case convention shared with reason labels above.
//
// The default branch panics (A-class state-machine unreachable) because a
// caller that passes an unrecognized Outcome is a programmer error: Outcome
// is an enumeration whose members are all handled above. Silently returning
// "unknown" would pollute metric series with invalid labels that mask genuine
// bugs; fail-closed is the correct behavior here.
//
// The result/reason labels (TickResult / DriveResult / LeaderSkipReason) are
// already string-typed enums produced exclusively by the Coordinator's
// classification helpers, so they cast directly via string(); their value sets
// are frozen by archtest SAGA-METRIC-LABEL-VALUES-FROZEN-01 instead of a panic
// switch. Outcome is an int enum, hence this explicit mapping.
func outcomeLabel(o executor.Outcome) string {
	switch o {
	case executor.OutcomeSucceeded:
		return "succeeded"
	case executor.OutcomeFailed:
		return "failed"
	case executor.OutcomeExpired:
		return "expired"
	case executor.OutcomeCanceled:
		return "canceled"
	case executor.OutcomeLeaseLost:
		return "lease_lost"
	default:
		panic(panicregister.Approved("saga-outcome-unknown",
			errcode.Assertion("saga metrics: unknown Outcome value %d", o)))
	}
}
