package otel_test

import (
	"context"
	"errors"
	"testing"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newTestProvider wires the SUT's NewMetricProvider to a fresh ManualReader.
//
// ref: opentelemetry-go sdk/metric/manual_reader.go@main — ManualReader is
// the canonical on-demand reader used by the SDK's own unit tests; avoids
// any network / OTLP exporter, keeping tests deterministic and fast.
func newTestProvider(t *testing.T) (metrics.Provider, func() metricdata.ResourceMetrics) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	meter := mp.Meter("gocell.test")
	p, err := gcotel.NewMetricProvider(meter)
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}
	collect := func() metricdata.ResourceMetrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("reader.Collect: %v", err)
		}
		return rm
	}
	return p, collect
}

func TestOTelMetricProvider_CounterInc(t *testing.T) {
	p, collect := newTestProvider(t)

	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "gocell_test_counter_total",
		Help:       "Test counter.",
		LabelNames: []string{"outcome"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	cv.With(metrics.Labels{"outcome": "success"}).Inc()
	cv.With(metrics.Labels{"outcome": "success"}).Inc()
	cv.With(metrics.Labels{"outcome": "failure"}).Add(3)

	rm := collect()
	sum, points := extractCounterSum(t, rm, "gocell_test_counter_total")
	if sum != 5 {
		t.Fatalf("counter total sum = %v, want 5", sum)
	}
	if points != 2 {
		t.Fatalf("counter distinct label sets = %d, want 2", points)
	}
}

func TestOTelMetricProvider_HistogramObserve(t *testing.T) {
	p, collect := newTestProvider(t)
	hv, err := p.HistogramVec(metrics.HistogramOpts{
		Name:       "gocell_test_hist_seconds",
		Help:       "Test histogram.",
		LabelNames: []string{"phase"},
		Buckets:    []float64{0.1, 1, 10},
	})
	if err != nil {
		t.Fatalf("HistogramVec: %v", err)
	}
	hv.With(metrics.Labels{"phase": "start"}).Observe(0.05)
	hv.With(metrics.Labels{"phase": "start"}).Observe(2.5)

	rm := collect()
	count, sum := extractHistogram(t, rm, "gocell_test_hist_seconds")
	if count != 2 {
		t.Fatalf("histogram count = %v, want 2", count)
	}
	if sum < 2.5 {
		t.Fatalf("histogram sum = %v, want >= 2.5", sum)
	}
}

func TestOTelMetricProvider_LabelMismatchPanics(t *testing.T) {
	p, _ := newTestProvider(t)
	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "gocell_test_mismatch_total",
		LabelNames: []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		recErr, ok := r.(error)
		if !ok || !errors.Is(recErr, metrics.ErrLabelMismatch) {
			t.Fatalf("panic must wrap metrics.ErrLabelMismatch, got %v", r)
		}
	}()
	cv.With(metrics.Labels{"a": "x"}) // missing "b"
}

func TestOTelMetricProvider_NilMeterRejected(t *testing.T) {
	if _, err := gcotel.NewMetricProvider(nil); err == nil {
		t.Fatal("nil meter must be rejected")
	}
}

func TestOTelMetricProvider_AttrCacheReuse(t *testing.T) {
	// Sanity check: same Labels → same MeasurementOption (exercises the
	// cache path; observable behavior is that repeat emissions still land
	// in the same data point, not that we can detect the reuse itself).
	p, collect := newTestProvider(t)
	cv, err := p.CounterVec(metrics.CounterOpts{
		Name:       "gocell_test_cache_total",
		LabelNames: []string{"k"},
	})
	if err != nil {
		t.Fatalf("CounterVec: %v", err)
	}
	for range 100 {
		cv.With(metrics.Labels{"k": "v"}).Inc()
	}
	_, points := extractCounterSum(t, collect(), "gocell_test_cache_total")
	if points != 1 {
		t.Fatalf("repeat Labels must collapse to 1 data point, got %d", points)
	}
}

// extractCounterSum returns total sum across all data points and the number
// of distinct attribute sets.
func extractCounterSum(t *testing.T, rm metricdata.ResourceMetrics, name string) (float64, int) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Sum[float64])
			if !ok {
				t.Fatalf("metric %s is not Sum[float64], got %T", name, m.Data)
			}
			var total float64
			for _, dp := range data.DataPoints {
				total += dp.Value
			}
			return total, len(data.DataPoints)
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0, 0
}

// extractHistogram returns aggregate count + sum across all data points.
func extractHistogram(t *testing.T, rm metricdata.ResourceMetrics, name string) (uint64, float64) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %s is not Histogram[float64], got %T", name, m.Data)
			}
			var count uint64
			var sum float64
			for _, dp := range data.DataPoints {
				count += dp.Count
				sum += dp.Sum
			}
			return count, sum
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0, 0
}
