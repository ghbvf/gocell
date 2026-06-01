package reconcile

import (
	"context"
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// Metric family names (FR-010). Single source so the Loop, RegisterMetrics, and
// any dashboard/alert reference the same identifiers.
const (
	metricReconcileTotal    = "reconcile_total"
	metricReconcileDuration = "reconcile_duration_seconds"
	metricReconcileInFlight = "reconcile_in_flight"
	metricReconcileLeader   = "reconcile_leader"
)

// Metric label names.
const (
	labelReconciler = "reconciler"
	labelResult     = "result"
)

// resultLabel is the sealed (package-private) string type for the
// reconcile_total{result=...} label. recordResult and classify deal only in
// resultLabel, so the value set is enumerable by TYPE (not by name prefix) and
// a stray string variable cannot reach the metric. The remaining hole — an
// untyped string literal is assignable to resultLabel — is closed downstream by
// the RECONCILE-RESULT-LABEL-VALUES-FROZEN-01 callsite guard, which bans any
// recordResult argument that is not one of the declared result* consts.
type resultLabel string

// Reconcile result label values for reconcile_total{result=...}. This is the
// frozen value set (FR-010): success / transient / permanent / skipped. A
// recovered reconciler panic is classified transient (it is retryable); there
// is no separate "panic" label — recovery.go::recoverReconcile wraps panics as
// transient errors and classify() routes them to resultTransient (PR-A5 #1166).
const (
	resultSuccess   resultLabel = "success"
	resultTransient resultLabel = "transient"
	resultPermanent resultLabel = "permanent"
	// resultSkipped is recorded when a duplicate trigger arrives for an entity
	// that is already being reconciled (F5 dirty/processing dedup). The trigger
	// is NOT dropped: it is coalesced into a pending dirty re-run so the entity
	// is guaranteed to be re-reconciled once the in-flight reconcile completes.
	// "skipped" means "this specific trigger was absorbed into the dirty set",
	// not "the entity will be skipped".
	resultSkipped resultLabel = "skipped"
)

// reconcileDurationBuckets are explicit upper bounds (seconds) for
// reconcile_duration_seconds. Reconcile spans range from sub-millisecond
// (no-op level check) to multi-second (DB write + external call), so the set
// covers ms→minute. Supplied explicitly because the metric leaves kernel
// (HistogramOpts godoc: callers should not rely on adapter defaults).
var reconcileDurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}

// Metrics holds the optional pre-bound instruments the Loop records to. A nil
// field disables that instrument (the Loop nil-checks before every record).
// Build via RegisterMetrics at the composition root and inject; the Loop
// preflights label sets at Start (same wiring shape as
// runtime/command.preflightSweepErrorCounter).
type Metrics struct {
	// Total counts reconcile outcomes, labels {reconciler, result}.
	Total kernelmetrics.CounterVec
	// Duration observes reconcile wall-clock seconds, labels {reconciler}.
	Duration kernelmetrics.HistogramVec
	// InFlight tracks concurrently-running reconciles, labels {reconciler}.
	InFlight kernelmetrics.GaugeVec
	// Leader is 1 when this instance owns the reconcile lease, else 0,
	// labels {reconciler}. In the single-process Loop it is set to 1 at Start;
	// real lease gating is the leader-election PR's concern.
	Leader kernelmetrics.GaugeVec
}

// errRegisterMetricFmt is the format string for metric registration errors.
// The two %s verbs are the metric name and the underlying error.
const errRegisterMetricFmt = "reconcile: register %s: %w"

// RegisterMetrics registers the four reconcile instruments on p with canonical
// names, labels, and buckets, returning them bundled. It is the single source
// for reconcile metric identity; consumers call it at the composition root and
// inject the result into a Loop (or the Builder does so).
func RegisterMetrics(p kernelmetrics.Provider) (Metrics, error) {
	total, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricReconcileTotal,
		Help:       "Total reconcile attempts by outcome.",
		LabelNames: []string{labelReconciler, labelResult},
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricReconcileTotal, err)
	}
	duration, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       metricReconcileDuration,
		Help:       "Reconcile call wall-clock duration in seconds.",
		LabelNames: []string{labelReconciler},
		Buckets:    reconcileDurationBuckets,
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricReconcileDuration, err)
	}
	inFlight, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       metricReconcileInFlight,
		Help:       "Reconciles currently executing.",
		LabelNames: []string{labelReconciler},
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricReconcileInFlight, err)
	}
	leader, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       metricReconcileLeader,
		Help:       "1 when this instance holds the reconcile lease, else 0.",
		LabelNames: []string{labelReconciler},
	})
	if err != nil {
		return Metrics{}, fmt.Errorf(errRegisterMetricFmt, metricReconcileLeader, err)
	}
	return Metrics{Total: total, Duration: duration, InFlight: inFlight, Leader: leader}, nil
}

// preflight probes each non-nil instrument's With() with the exact label set the
// Loop uses, under recover. With() panics (MustValidateLabels) on a label-set
// mismatch; without this, the first record would crash a worker goroutine and
// crashloop the service. Catching it here turns a misconfigured vec into a
// fail-fast Start error. The recovered value is redacted before wrapping so a
// credential-bearing panic string cannot leak. Mirrors
// runtime/command.preflightSweepErrorCounter.
func (m Metrics) preflight(reconcilerID string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			redacted := redaction.RedactError(fmt.Errorf("%v", r))
			err = fmt.Errorf("reconcile: metrics label set invalid (reconciler=%q): %w", reconcilerID, redacted)
		}
	}()
	if m.Total != nil {
		_ = m.Total.With(kernelmetrics.Labels{labelReconciler: reconcilerID, labelResult: string(resultSuccess)})
	}
	if m.Duration != nil {
		_ = m.Duration.With(kernelmetrics.Labels{labelReconciler: reconcilerID})
	}
	if m.InFlight != nil {
		_ = m.InFlight.With(kernelmetrics.Labels{labelReconciler: reconcilerID})
	}
	if m.Leader != nil {
		_ = m.Leader.With(kernelmetrics.Labels{labelReconciler: reconcilerID})
	}
	return nil
}

// recordResult increments reconcile_total{reconciler, result} when wired. The
// result parameter is the sealed resultLabel type; the archtest callsite guard
// (RECONCILE-RESULT-LABEL-VALUES-FROZEN-01) additionally bans any caller passing
// a value that is not one of the declared result* consts.
func (m Metrics) recordResult(ctx context.Context, reconcilerID string, result resultLabel) {
	if m.Total == nil {
		return
	}
	m.Total.With(kernelmetrics.Labels{labelReconciler: reconcilerID, labelResult: string(result)}).Inc(ctx)
}

// observeDuration records reconcile_duration_seconds{reconciler} when wired.
func (m Metrics) observeDuration(ctx context.Context, reconcilerID string, seconds float64) {
	if m.Duration == nil {
		return
	}
	m.Duration.With(kernelmetrics.Labels{labelReconciler: reconcilerID}).Observe(ctx, seconds)
}

// inFlightDelta adds delta to reconcile_in_flight{reconciler} when wired.
func (m Metrics) inFlightDelta(ctx context.Context, reconcilerID string, delta float64) {
	if m.InFlight == nil {
		return
	}
	m.InFlight.With(kernelmetrics.Labels{labelReconciler: reconcilerID}).Add(ctx, delta)
}

// setLeader sets reconcile_leader{reconciler} when wired.
func (m Metrics) setLeader(ctx context.Context, reconcilerID string, value float64) {
	if m.Leader == nil {
		return
	}
	m.Leader.With(kernelmetrics.Labels{labelReconciler: reconcilerID}).Set(ctx, value)
}
