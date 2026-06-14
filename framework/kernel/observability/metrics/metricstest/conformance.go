// Package metricstest provides the cross-implementation conformance harness for
// the kernel metrics no-skip-on-cancel contract.
//
// kernel/observability/metrics/metrics.go §contract (1): "实现禁止因 ctx 已取消/
// 超时而跳过记录——ctx 取消不得导致数据丢失". This harness drives a metrics.Provider's
// Counter/Histogram/Gauge instruments with an already-canceled context and
// asserts — via an implementation-supplied Readback — that every measurement
// still landed. Membership of every production Provider implementation is
// enforced by archtest METRICS-CANCEL-CTX-CONFORMANCE-01.
//
// Stdlib testing asserts only: kernel/ depguard bans testify, so this harness
// uses t.Fatalf/t.Errorf rather than require/assert.
package metricstest

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// Metric names the harness records under. Adapter-side Readback closures target
// these (prefixed by the provider's namespace, if any).
const (
	CounterName   = "metricstest_cancelctx_counter_total"
	HistogramName = "metricstest_cancelctx_hist_seconds"
	GaugeName     = "metricstest_cancelctx_gauge"

	labelKey = "k"
	labelVal = "v"

	// Expected recorded values after the canceled-ctx drive sequence below.
	wantCounter   = 3 // Add(2) + Inc
	wantHistogram = 1 // one Observe
	wantGauge     = 8 // Set(5) + Inc - Dec + Add(3)
)

// Labels is the single label set the harness binds for all three instruments.
func Labels() metrics.Labels { return metrics.Labels{labelKey: labelVal} }

// Readback supplies implementation-specific value extraction for the harness's
// metrics under Labels(). Each closure returns the recorded value; the harness
// asserts them against the expected post-drive totals.
type Readback struct {
	Counter   func() float64
	Histogram func() uint64 // observation count
	Gauge     func() float64
}

// RunCanceledCtxConformance asserts that p's instruments record measurements
// even when the supplied context is already canceled (metrics.go §contract (1)).
// It drives Counter.Add/Inc, Histogram.Observe, and Gauge.Set/Inc/Dec/Add with an
// already-canceled ctx, then uses rb to assert each measurement landed. A
// regression that gates recording on ctx.Err() makes a readback return the wrong
// value and fails here — a no-op implementation cannot satisfy it.
func RunCanceledCtxConformance(t *testing.T, p metrics.Provider, rb Readback) {
	t.Helper()
	if rb.Counter == nil || rb.Histogram == nil || rb.Gauge == nil {
		t.Fatal("metricstest: Readback.Counter/Histogram/Gauge must all be supplied")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // every record call below observes ctx.Err() != nil

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name: CounterName, Help: "cancel-ctx conformance counter", LabelNames: []string{labelKey},
	})
	if err != nil {
		t.Fatalf("metricstest: CounterVec: %v", err)
	}
	cv.With(Labels()).Add(ctx, 2)
	cv.With(Labels()).Inc(ctx)

	hv, err := p.HistogramVec(metrics.HistogramOpts{
		Name: HistogramName, Help: "cancel-ctx conformance histogram",
		LabelNames: []string{labelKey}, Buckets: []float64{0.5, 1, 2},
	})
	if err != nil {
		t.Fatalf("metricstest: HistogramVec: %v", err)
	}
	hv.With(Labels()).Observe(ctx, 1.0)

	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name: GaugeName, Help: "cancel-ctx conformance gauge", LabelNames: []string{labelKey},
	})
	if err != nil {
		t.Fatalf("metricstest: GaugeVec: %v", err)
	}
	gv.With(Labels()).Set(ctx, 5)
	gv.With(Labels()).Inc(ctx)
	gv.With(Labels()).Dec(ctx)
	gv.With(Labels()).Add(ctx, 3)

	if got := rb.Counter(); got != wantCounter {
		t.Errorf("metricstest: counter recorded %v under canceled ctx, want %d "+
			"(no-skip-on-cancel violated: Add/Inc must not gate on ctx.Err())", got, wantCounter)
	}
	if got := rb.Histogram(); got != wantHistogram {
		t.Errorf("metricstest: histogram recorded count %d under canceled ctx, want %d "+
			"(no-skip-on-cancel violated: Observe must not gate on ctx.Err())", got, wantHistogram)
	}
	if got := rb.Gauge(); got != wantGauge {
		t.Errorf("metricstest: gauge recorded %v under canceled ctx, want %d "+
			"(no-skip-on-cancel violated: Set/Inc/Dec/Add must not gate on ctx.Err())", got, wantGauge)
	}
}
