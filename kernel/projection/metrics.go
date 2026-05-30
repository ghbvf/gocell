package projection

import (
	"context"
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// Metric family names. Single source so the Coordinator, RegisterMetrics, and
// any dashboard/alert reference the same identifiers.
const (
	metricProjectionReplayLag       = "projection_event_replay_lag_seconds"
	metricProjectionRebuildDuration = "projection_rebuild_duration_seconds"
	metricProjectionPendingEvents   = "projection_pending_events"
)

// Metric label names.
const (
	labelCell       = "cell"
	labelProjection = "projection"
)

// projectionRebuildDurationBuckets covers sub-millisecond to multi-minute
// rebuild durations. Smaller projections complete in ms; large historical
// replays may take minutes.
var projectionRebuildDurationBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300}

// Metrics holds the optional pre-bound instruments the Coordinator records to.
// A nil *Metrics (or nil individual fields) disables that instrument. Build via
// RegisterMetrics and inject into NewCoordinator as an optional parameter
// (nil = instruments disabled, cloned from kernel/reconcile/metrics.go pattern).
type Metrics struct {
	// ReplayLag is a gauge tracking the event lag in seconds:
	// lag = now − lastApplied.OccurredAt(). Labels: {cell, projection}.
	ReplayLag kernelmetrics.GaugeVec
	// RebuildDuration is a histogram observing rebuild wall-clock duration in
	// seconds. Labels: {cell, projection}.
	RebuildDuration kernelmetrics.HistogramVec
	// PendingEvents is a gauge tracking replay.Head() − checkpoint: the number
	// of events not yet reflected in the projection. Labels: {cell, projection}.
	PendingEvents kernelmetrics.GaugeVec
}

// RegisterMetrics registers the three projection instruments on p with canonical
// names, labels, and buckets, returning them bundled. Call at the composition
// root and inject the result into NewCoordinator.
func RegisterMetrics(p kernelmetrics.Provider) (*Metrics, error) {
	lag, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       metricProjectionReplayLag,
		Help:       "Event replay lag in seconds (now − lastApplied.OccurredAt). Labels: cell, projection.",
		LabelNames: []string{labelCell, labelProjection},
	})
	if err != nil {
		return nil, fmt.Errorf("projection: register %s: %w", metricProjectionReplayLag, err)
	}
	dur, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       metricProjectionRebuildDuration,
		Help:       "Projection rebuild wall-clock duration in seconds. Labels: cell, projection.",
		LabelNames: []string{labelCell, labelProjection},
		Buckets:    projectionRebuildDurationBuckets,
	})
	if err != nil {
		return nil, fmt.Errorf("projection: register %s: %w", metricProjectionRebuildDuration, err)
	}
	pending, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       metricProjectionPendingEvents,
		Help:       "Number of events not yet reflected in the projection (Head − checkpoint). Labels: cell, projection.",
		LabelNames: []string{labelCell, labelProjection},
	})
	if err != nil {
		return nil, fmt.Errorf("projection: register %s: %w", metricProjectionPendingEvents, err)
	}
	return &Metrics{ReplayLag: lag, RebuildDuration: dur, PendingEvents: pending}, nil
}

// preflight probes each non-nil instrument's With() with the exact label set
// the Coordinator uses, under recover. With() panics (MustValidateLabels) on a
// label-set mismatch; catching it here turns misconfiguration into a fail-fast
// error rather than a mid-flight goroutine panic. Mirrors reconcile.Metrics.preflight.
func (m *Metrics) preflight(cellID, projectionID string) (err error) {
	if m == nil {
		return nil
	}
	defer func() {
		if r := recover(); r != nil {
			redacted := redaction.RedactError(fmt.Errorf("%v", r))
			err = fmt.Errorf("projection: metrics label set invalid (cell=%q projection=%q): %w", cellID, projectionID, redacted)
		}
	}()
	l := kernelmetrics.Labels{labelCell: cellID, labelProjection: projectionID}
	if m.ReplayLag != nil {
		_ = m.ReplayLag.With(l)
	}
	if m.RebuildDuration != nil {
		_ = m.RebuildDuration.With(l)
	}
	if m.PendingEvents != nil {
		_ = m.PendingEvents.With(l)
	}
	return nil
}

// setReplayLag sets projection_event_replay_lag_seconds{cell, projection} = seconds.
// Nil-safe: no-op when m or m.ReplayLag is nil.
func (m *Metrics) setReplayLag(ctx context.Context, cellID, projectionID string, seconds float64) {
	if m == nil || m.ReplayLag == nil {
		return
	}
	m.ReplayLag.With(kernelmetrics.Labels{labelCell: cellID, labelProjection: projectionID}).Set(ctx, seconds)
}

// observeRebuildDuration records projection_rebuild_duration_seconds{cell, projection}.
// Nil-safe: no-op when m or m.RebuildDuration is nil.
func (m *Metrics) observeRebuildDuration(ctx context.Context, cellID, projectionID string, seconds float64) {
	if m == nil || m.RebuildDuration == nil {
		return
	}
	m.RebuildDuration.With(kernelmetrics.Labels{labelCell: cellID, labelProjection: projectionID}).Observe(ctx, seconds)
}

// setPendingEvents sets projection_pending_events{cell, projection} = n.
// Nil-safe: no-op when m or m.PendingEvents is nil.
func (m *Metrics) setPendingEvents(ctx context.Context, cellID, projectionID string, n float64) {
	if m == nil || m.PendingEvents == nil {
		return
	}
	m.PendingEvents.With(kernelmetrics.Labels{labelCell: cellID, labelProjection: projectionID}).Set(ctx, n)
}
