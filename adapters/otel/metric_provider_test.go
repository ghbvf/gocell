package otel_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
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

// TestMetricProvider_GaugeVec_Register verifies that GaugeVec returns a
// non-nil, non-error result for a valid GaugeOpts.
func TestMetricProvider_GaugeVec_Register(t *testing.T) {
	p, _ := newTestProvider(t)
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge",
		Help:       "Test gauge.",
		LabelNames: []string{"state"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}
	if gv == nil {
		t.Fatal("GaugeVec returned nil vec")
	}
	if !gv.Registered() {
		t.Fatal("Registered() must return true")
	}
}

// TestMetricProvider_GaugeVec_RecordsViaUpDownCounter verifies that Set()
// correctly emulates last-value semantics via a Float64UpDownCounter:
// Set(10) then Set(20) must produce a single cumulative value of 20.
func TestMetricProvider_GaugeVec_RecordsViaUpDownCounter(t *testing.T) {
	p, collect := newTestProvider(t)
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge_set",
		Help:       "Test gauge set.",
		LabelNames: []string{"instance"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	g := gv.With(metrics.Labels{"instance": "a"})
	g.Set(10)
	g.Set(20) // delta = +10 → cumulative = 20

	rm := collect()
	val, points := extractGaugeSum(t, rm, "gocell_test_gauge_set")
	if val != 20 {
		t.Fatalf("gauge cumulative value = %v, want 20", val)
	}
	if points != 1 {
		t.Fatalf("gauge data points = %d, want 1", points)
	}
}

// TestMetricProvider_GaugeVec_IncDec verifies that Inc then Dec results in
// a cumulative value of zero.
func TestMetricProvider_GaugeVec_IncDec(t *testing.T) {
	p, collect := newTestProvider(t)
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge_incdec",
		Help:       "Test gauge inc dec.",
		LabelNames: []string{"worker"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	g := gv.With(metrics.Labels{"worker": "w1"})
	g.Inc()
	g.Dec()

	rm := collect()
	val, _ := extractGaugeSum(t, rm, "gocell_test_gauge_incdec")
	if val != 0 {
		t.Fatalf("Inc then Dec must yield 0, got %v", val)
	}
}

// TestMetricProvider_GaugeVec_Add_Positive_And_Negative verifies that
// Add with positive and negative deltas correctly updates the gauge.
func TestMetricProvider_GaugeVec_Add_Positive_And_Negative(t *testing.T) {
	p, collect := newTestProvider(t)
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge_add",
		Help:       "Test gauge add.",
		LabelNames: []string{"queue"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	g := gv.With(metrics.Labels{"queue": "main"})
	g.Add(5)
	g.Add(-3)

	rm := collect()
	val, _ := extractGaugeSum(t, rm, "gocell_test_gauge_add")
	if val != 2 {
		t.Fatalf("Add(5)+Add(-3) must yield 2, got %v", val)
	}
}

// TestMetricProvider_GaugeVec_DistinctLabelSetsEmitted verifies that distinct
// label sets produce distinct data points in the OTel output for GaugeVec.
//
// Note: overflow behavior (otel.metric.overflow=true bucket when cap is
// exceeded) is tested by the package-internal test
// TestMetricProvider_OverflowDataPointEmitted, which can directly set the
// unexported attrCacheMaxSize field. This external test's purpose is to confirm
// that N distinct label sets each produce N distinct data points through the
// full GaugeVec wire path — it does not and cannot test overflow cap semantics
// from package otel_test.
func TestMetricProvider_GaugeVec_DistinctLabelSetsEmitted(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	p, err := gcotel.NewMetricProvider(mp.Meter("gocell.test.gauge.distinct"))
	if err != nil {
		t.Fatalf("NewMetricProvider: %v", err)
	}

	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge_distinct",
		Help:       "distinct label sets test",
		LabelNames: []string{"k"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	// Emit 3 distinct label sets; verify each produces a distinct data point.
	for i := range 3 {
		gv.With(metrics.Labels{"k": strconv.Itoa(i)}).Set(float64(i + 1))
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var totalPoints int
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == "gocell_test_gauge_distinct" {
				data, ok := m.Data.(metricdata.Gauge[float64])
				if !ok {
					// UpDownCounter reports as Sum, not Gauge — check both.
					sumData, ok2 := m.Data.(metricdata.Sum[float64])
					if !ok2 {
						t.Fatalf("metric data is %T, want Gauge or Sum", m.Data)
					}
					totalPoints = len(sumData.DataPoints)
				} else {
					totalPoints = len(data.DataPoints)
				}
			}
		}
	}
	if totalPoints != 3 {
		t.Fatalf("expected 3 distinct data points for 3 distinct label sets, got %d", totalPoints)
	}
}

// TestMetricProvider_GaugeVec_SetAfterInc verifies the `last` field invariant
// under mixed Set/Inc call ordering:
//
//	Set(5)  → last=5, delta=+5, cumulative=5
//	Inc()   → last=6, delta=+1, cumulative=6
//	Set(3)  → last=3, delta=-3, cumulative=3
//
// The final cumulative value must be 3 (the last Set value). This ensures that
// Set after Inc correctly computes the delta relative to the tracked `last`
// value, not relative to zero.
func TestMetricProvider_GaugeVec_SetAfterInc(t *testing.T) {
	p, collect := newTestProvider(t)
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "set_after_inc_test",
		Help:       "Test Set+Inc mixed ordering.",
		LabelNames: []string{"svc"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	g := gv.With(metrics.Labels{"svc": "a"})
	g.Set(5) // last=5, delta=+5, cum=5
	g.Inc()  // last=6, delta=+1, cum=6
	g.Set(3) // last=3, delta=-3, cum=3

	val, _ := extractGaugeSum(t, collect(), "set_after_inc_test")
	if val != 3 {
		t.Fatalf("Set(5)+Inc()+Set(3) must yield cumulative 3, got %v", val)
	}
}

// TestMetricProvider_ConcurrentGaugeVec_RaceDetector runs concurrent With /
// Set calls on the same GaugeVec to detect data races under -race.
func TestMetricProvider_ConcurrentGaugeVec_RaceDetector(t *testing.T) {
	p, _ := newTestProvider(t)
	gv, err := p.GaugeVec(metrics.GaugeOpts{
		Name:       "gocell_test_gauge_race",
		Help:       "concurrent race test",
		LabelNames: []string{"shard"},
	})
	if err != nil {
		t.Fatalf("GaugeVec: %v", err)
	}

	const goroutines = 20
	const iters = 50
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// All goroutines share two label sets to exercise the
			// same-slot concurrent write path.
			shard := strconv.Itoa(id % 2)
			gauge := gv.With(metrics.Labels{"shard": shard})
			for i := range iters {
				switch i % 4 {
				case 0:
					gauge.Set(float64(i))
				case 1:
					gauge.Inc()
				case 2:
					gauge.Dec()
				case 3:
					gauge.Add(float64(i))
				}
			}
		}(g)
	}
	wg.Wait()
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

// extractGaugeSum returns the total cumulative value across all data points
// of a Float64UpDownCounter (the OTel instrument backing GaugeVec) and the
// number of distinct attribute sets. UpDownCounter reports as
// metricdata.Sum[float64] with Temporality=Cumulative, IsMonotonic=false.
func extractGaugeSum(t *testing.T, rm metricdata.ResourceMetrics, name string) (float64, int) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			data, ok := m.Data.(metricdata.Sum[float64])
			if !ok {
				t.Fatalf("gauge metric %s is not Sum[float64] (UpDownCounter), got %T", name, m.Data)
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
