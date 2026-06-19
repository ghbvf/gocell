package refresh

import (
	"context"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// GCCollector records refresh-token GC outcomes. Alert on sustained
// auth_refresh_gc_runs_total{result="failure"} growth.
type GCCollector interface {
	ObserveRefreshGC(ctx context.Context, result string, removed int, duration time.Duration)
}

// NoopGCCollector drops all GC observations.
type NoopGCCollector struct{}

func (NoopGCCollector) ObserveRefreshGC(context.Context, string, int, time.Duration) {
	// Intentionally empty: used when refresh GC metrics are disabled.
}

// ProviderGCCollector records refresh GC metrics through the kernel metrics API.
type ProviderGCCollector struct {
	runs     metrics.CounterVec
	removed  metrics.CounterVec
	duration metrics.HistogramVec
}

func NewProviderGCCollector(p metrics.Provider) (*ProviderGCCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid, "refresh gc metrics provider must not be nil")
	}

	runs, err := p.CounterVec(metrics.CounterOpts{
		Name:       "auth_refresh_gc_runs_total",
		Help:       "Total number of refresh-token GC runs by result.",
		LabelNames: []string{"result"},
	})
	if err != nil {
		return nil, fmt.Errorf("refresh gc: register auth_refresh_gc_runs_total: %w", err)
	}
	removed, err := p.CounterVec(metrics.CounterOpts{
		Name:       "auth_refresh_gc_removed_total",
		Help:       "Total number of refresh-token rows removed by GC.",
		LabelNames: []string{"result"},
	})
	if err != nil {
		return nil, fmt.Errorf("refresh gc: register auth_refresh_gc_removed_total: %w", err)
	}
	duration, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "auth_refresh_gc_duration_seconds",
		Help:       "Duration of refresh-token GC runs in seconds.",
		LabelNames: []string{"result"},
		Buckets:    []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
	})
	if err != nil {
		return nil, fmt.Errorf("refresh gc: register auth_refresh_gc_duration_seconds: %w", err)
	}
	return &ProviderGCCollector{runs: runs, removed: removed, duration: duration}, nil
}

func (c *ProviderGCCollector) ObserveRefreshGC(ctx context.Context, result string, removed int, duration time.Duration) {
	if c == nil {
		return
	}
	labels := metrics.Labels{"result": result}
	c.runs.With(labels).Inc(ctx)
	c.removed.With(labels).Add(ctx, float64(removed))
	c.duration.With(labels).Observe(ctx, duration.Seconds())
}
