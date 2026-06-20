package metrics

import (
	"context"
	"time"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/saga/tailer"
)

// SagaTailerCollector registers the saga-journal Tailer's operational metrics
// scoped to the owner cell supplied at construction. One collector per cell; the
// projection label varies per call (a cell may run a Tailer per projection). It
// satisfies tailer.Observer (ADR 202606051200-1609 §4.3 observability checklist).
//
// Counters:
//   - saga_journal_tailer_lock_acquire_failed_total{cell,projection,reason}:
//     per-projection distlock acquire failures (the leader gate skipped the
//     tick), reason ∈ {contended, ctx_canceled, backend_error}. backend_error is
//     the lock-acquire failure rate (drives the alert); contended is benign
//     multi-process contention.
//   - saga_journal_tailer_drain_total{cell,projection,result}: drains that made
//     progress or failed, result ∈ {ok, head_error, store_error, apply_error}.
//     Idle caught-up ticks are not counted.
//   - saga_journal_tailer_checkpoint_advance_total{cell,projection,result}:
//     AdvanceIfOwner attempts, result ∈ {ok, stale_owner, error}. stale_owner is a
//     benign leader handoff (exclude from advance-failure alerting).
//
// Gauges:
//   - saga_journal_tailer_pending_events{cell,projection}: residual backlog
//     (HeadSeq − checkpoint) after the last clean tick.
//   - saga_journal_tailer_last_success_timestamp_seconds{cell,projection}: unix
//     time of the last fully-completed tick (drives the stalled-tailer alert).
//
// Cardinality discipline: cell × projection × (bounded reason/result enum
// values). Both cell and projection are assembly-enumerated static sets;
// per-event identities (event id, owner token) are never label dimensions.
type SagaTailerCollector struct {
	cellID string

	lockFail    kernelmetrics.CounterVec // saga_journal_tailer_lock_acquire_failed_total{cell,projection,reason}
	drain       kernelmetrics.CounterVec // saga_journal_tailer_drain_total{cell,projection,result}
	advance     kernelmetrics.CounterVec // saga_journal_tailer_checkpoint_advance_total{cell,projection,result}
	pending     kernelmetrics.GaugeVec   // saga_journal_tailer_pending_events{cell,projection}
	lastSuccess kernelmetrics.GaugeVec   // saga_journal_tailer_last_success_timestamp_seconds{cell,projection}
}

// compile-time interface check.
var _ tailer.Observer = (*SagaTailerCollector)(nil)

// NewSagaTailerCollector registers the Tailer metrics on the given provider.
// cellID is the owner cell; empty is an error (no _runtime fallback — a Tailer is
// always owned by exactly one cell). Any registration failure is startup-fatal
// for the current wiring; caller owns provider lifecycle cleanup.
//
// Caller contract: never pass a nil *SagaTailerCollector — use tailer.NopObserver
// via WithObserver(nil) for explicit disable.
func NewSagaTailerCollector(p kernelmetrics.Provider, cellID string) (*SagaTailerCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SagaTailerCollector Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: SagaTailerCollector cellID is required")
	}
	c := &SagaTailerCollector{cellID: cellID}
	var err error
	if c.lockFail, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_journal_tailer_lock_acquire_failed_total",
		Help: "Total per-projection distlock acquire failures (the saga-journal tailer leader gate " +
			"skipped the tick), labeled by reason (contended = another process holds the lock; " +
			"ctx_canceled = shutdown; backend_error = distlock I/O fault / lock-acquire failure rate).",
		LabelNames: []string{"cell", "projection", "reason"},
	}); err != nil {
		return nil, err
	}
	if c.drain, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_journal_tailer_drain_total",
		Help: "Total saga-journal tailer drains that made progress or failed, labeled by result " +
			"(ok = ≥1 event applied; head_error = head-bound fetch failed; store_error = checkpoint " +
			"load failed; apply_error = non-stale replay/apply failure). Idle caught-up ticks are not counted.",
		LabelNames: []string{"cell", "projection", "result"},
	}); err != nil {
		return nil, err
	}
	if c.advance, err = registerCounterVec(p, kernelmetrics.CounterOpts{
		Name: "saga_journal_tailer_checkpoint_advance_total",
		Help: "Total saga-journal tailer checkpoint AdvanceIfOwner attempts, labeled by result " +
			"(ok = committed; stale_owner = deposed-leader fence, benign; error = tx/storage fault; " +
			"poison_skip = dead-lettered poison event, checkpoint advanced past it (#2110)). " +
			"Alert on result=error; exclude result=stale_owner.",
		LabelNames: []string{"cell", "projection", "result"},
	}); err != nil {
		return nil, err
	}
	if c.pending, err = registerGaugeVec(p, kernelmetrics.GaugeOpts{
		Name:       "saga_journal_tailer_pending_events",
		Help:       "Residual saga-journal tailer backlog (HeadSeq − checkpoint) after the last clean tick.",
		LabelNames: []string{"cell", "projection"},
	}); err != nil {
		return nil, err
	}
	if c.lastSuccess, err = registerGaugeVec(p, kernelmetrics.GaugeOpts{
		Name:       "saga_journal_tailer_last_success_timestamp_seconds",
		Help:       "Unix time of the last fully-completed saga-journal tailer tick (drives the stalled-tailer alert).",
		LabelNames: []string{"cell", "projection"},
	}); err != nil {
		return nil, err
	}
	return c, nil
}

// ObserveLockAcquire implements tailer.Observer.
func (c *SagaTailerCollector) ObserveLockAcquire(ctx context.Context, projectionID string, reason tailer.LockAcquireResult) {
	c.lockFail.With(kernelmetrics.Labels{
		"cell": c.cellID, "projection": projectionID, "reason": string(reason),
	}).Inc(ctx)
}

// ObserveDrain implements tailer.Observer.
func (c *SagaTailerCollector) ObserveDrain(ctx context.Context, projectionID string, result tailer.DrainResult) {
	c.drain.With(kernelmetrics.Labels{
		"cell": c.cellID, "projection": projectionID, "result": string(result),
	}).Inc(ctx)
}

// ObserveCheckpointAdvance implements tailer.Observer.
func (c *SagaTailerCollector) ObserveCheckpointAdvance(ctx context.Context, projectionID string, result tailer.AdvanceResult) {
	c.advance.With(kernelmetrics.Labels{
		"cell": c.cellID, "projection": projectionID, "result": string(result),
	}).Inc(ctx)
}

// ObserveLag implements tailer.Observer.
func (c *SagaTailerCollector) ObserveLag(ctx context.Context, projectionID string, pending int64) {
	c.pending.With(kernelmetrics.Labels{
		"cell": c.cellID, "projection": projectionID,
	}).Set(ctx, float64(pending))
}

// ObserveLastSuccess implements tailer.Observer.
func (c *SagaTailerCollector) ObserveLastSuccess(ctx context.Context, projectionID string, ts time.Time) {
	c.lastSuccess.With(kernelmetrics.Labels{
		"cell": c.cellID, "projection": projectionID,
	}).Set(ctx, float64(ts.Unix()))
}
