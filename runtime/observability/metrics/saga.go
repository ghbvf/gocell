package metrics

import (
	"context"
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/saga/executor"
)

// SagaStepCollector registers three saga step metrics scoped to the owner
// cell supplied at construction. One collector per Coordinator (= per-cell).
//
// Metrics registered:
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
// Cardinality discipline: definition_id × step_name is the worst case; both
// are static enumeration sets (registered at compile time). Expected upper
// bound: ≤ 200 active series per cell (≈ 20 definitions × 10 steps); metrics
// provider cap=2000 is the runtime tripwire.
//
// ref: temporalio/sdk-go internal_task_handlers.go — server-emitted
// activity outcome / heartbeat-failure metric envelope.
// ref: itimofeev/go-saga (no native metrics — pattern adapted from outbox).
type SagaStepCollector struct {
	cellID string

	outcome kernelmetrics.CounterVec // saga_step_outcome_total{cell,definition_id,outcome}
	retry   kernelmetrics.CounterVec // saga_step_retry_total{cell,definition_id,step_name}
	hbFail  kernelmetrics.CounterVec // saga_heartbeat_failed_total{cell,reason}
}

// compile-time interface check.
var _ executor.Observer = (*SagaStepCollector)(nil)

// NewSagaStepCollector registers the three saga counters on the given provider.
// cellID is the owner cell of the Coordinator wiring this collector; empty is
// an error (no fallback to _runtime sentinel — the saga Coordinator is always
// owned by exactly one cell).
//
// Failure modes:
//   - p == nil → errcode.KindInvalid + ErrObservabilityConfigInvalid
//   - cellID == "" → errcode.KindInvalid + ErrObservabilityConfigInvalid
//   - any CounterVec registration error is wrapped with the metric name
//
// Caller contract: never pass a nil *SagaStepCollector — use NopObserver via
// WithObserver(nil) for explicit disable.
func NewSagaStepCollector(p kernelmetrics.Provider, cellID string) (*SagaStepCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SagaStepCollector Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SagaStepCollector cellID is required")
	}

	// #1181 F11: atomic registration. If any CounterVec call fails mid-way,
	// the successfully-registered counters from earlier in the sequence are
	// torn down LIFO so the provider's registry does not retain orphans (a
	// subsequent retry must be free to re-register under the same names).
	// prometheus/client_golang Registry.Unregister provides the equivalent
	// rollback primitive that this mirrors.
	var registered []kernelmetrics.Collector
	rollbackOnErr := func() {
		for i := len(registered) - 1; i >= 0; i-- {
			_ = p.Unregister(registered[i])
		}
	}

	outcome, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name: "saga_step_outcome_total",
		Help: "Total saga step Execute outcomes (succeeded/failed/expired/canceled/lease_lost). " +
			"step_name is intentionally excluded to bound the step×outcome cardinality; " +
			"per-step drill-down uses saga_step_retry_total.",
		LabelNames: []string{"cell", "definition_id", "outcome"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register saga_step_outcome_total: %w", err)
	}
	registered = append(registered, outcome)

	retry, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "saga_step_retry_total",
		Help:       "Total retry attempts emitted by the saga executor (attempt N>1 fired) per definition+step.",
		LabelNames: []string{"cell", "definition_id", "step_name"},
	})
	if err != nil {
		rollbackOnErr()
		return nil, fmt.Errorf("runtime/observability/metrics: register saga_step_retry_total: %w", err)
	}
	registered = append(registered, retry)

	hbFail, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name: "saga_heartbeat_failed_total",
		Help: "Total heartbeat tick failures observed by the saga executor, labeled by reason " +
			"(infra_error = transient err; stale_lease = ok=false / another coordinator took over). " +
			"Excludes definition_id and step_name — heartbeat is an infra signal, not per-step.",
		LabelNames: []string{"cell", "reason"},
	})
	if err != nil {
		rollbackOnErr()
		return nil, fmt.Errorf("runtime/observability/metrics: register saga_heartbeat_failed_total: %w", err)
	}

	return &SagaStepCollector{
		cellID:  cellID,
		outcome: outcome,
		retry:   retry,
		hbFail:  hbFail,
	}, nil
}

// ObserveOutcome implements executor.Observer.
// Records one increment on saga_step_outcome_total{cell,definition_id,outcome}.
// attempts is not labeled (would explode cardinality) but is carried into
// trace spans and slog by the executor itself.
func (c *SagaStepCollector) ObserveOutcome(
	ctx context.Context,
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
func (c *SagaStepCollector) ObserveRetry(ctx context.Context, definitionID, stepName string) {
	c.retry.With(kernelmetrics.Labels{
		"cell":          c.cellID,
		"definition_id": definitionID,
		"step_name":     stepName,
	}).Inc(ctx)
}

// ObserveHeartbeatFailure implements executor.Observer.
// Records one increment on saga_heartbeat_failed_total{cell,reason}.
func (c *SagaStepCollector) ObserveHeartbeatFailure(ctx context.Context, reason executor.HeartbeatFailureReason) {
	c.hbFail.With(kernelmetrics.Labels{
		"cell":   c.cellID,
		"reason": string(reason),
	}).Inc(ctx)
}

// outcomeLabel maps an Outcome to its wire-stable lowercase label value.
// Using fmt.Stringer would emit "Succeeded" (camel); metric labels follow
// the snake_case convention shared with reason labels above.
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
		return "unknown"
	}
}
