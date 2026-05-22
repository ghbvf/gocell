//go:build archtest_fixture

// Package metricsgaugevecfixture provides a RED fixture for
// METRICS-GAUGEVEC-FUNNEL-01: a small package that directly imports
// prometheus / otel and calls the banned constructors. The archtest
// asserts these specific call sites are flagged.
//
// # Violations in this fixture
//
// BadPromGaugeVec: a direct prom.NewGaugeVec call — must be flagged.
//
// BadOtelUpDownCounter: a direct meter.Float64UpDownCounter call — must be flagged.
//
// BadOtelFloat64Gauge: a direct meter.Float64Gauge call — must be flagged
// after Wave 3 (PR #593 review fix-up) extends the OTel ban set to include
// the current adapter primitive. The ban set was previously limited to
// Float64UpDownCounter (the historical leak surface), which let business code
// bypass the GaugeVec funnel through Float64Gauge once the adapter migrated.
//
// BadPromCounter: a direct prom.NewCounter call — must be flagged after B2
// extends the prom ban set to all prom.New* constructors.
//
// BadPromCounterVec: a direct prom.NewCounterVec call — must be flagged.
//
// BadPromHistogramVec: a direct prom.NewHistogramVec call — must be flagged.
//
// BadOtelFloat64Counter: a direct meter.Float64Counter call — must be flagged
// after B2 extends the OTel ban set to all synchronous Float64*/Int64* methods.
//
// BadOtelFloat64Histogram: a direct meter.Float64Histogram call — must be flagged.
//
// BadOtelInt64Counter: a direct meter.Int64Counter call — must be flagged
// (defensive ban; no production callsite yet, but the method is part of the
// full synchronous meter surface covered by the funnel).
//
// GREEN controls: any call that does NOT hit the banned symbols must produce
// zero diagnostics (none present in this fixture by design).
//
// Total expected diagnostics: 9 (three prom + six otel).
package metricsgaugevecfixture

import (
	prom "github.com/prometheus/client_golang/prometheus"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// BadPromGaugeVec is a direct prom.NewGaugeVec call — must be flagged by
// METRICS-GAUGEVEC-FUNNEL-01. Production code must route through
// kernel/observability/metrics.Provider.GaugeVec instead.
func BadPromGaugeVec() prom.Collector {
	return prom.NewGaugeVec(prom.GaugeOpts{Name: "bad_gauge", Help: "fixture"}, []string{"label"})
}

// BadOtelUpDownCounter is a direct meter.Float64UpDownCounter call — must be
// flagged by METRICS-GAUGEVEC-FUNNEL-01. Production code must route through
// kernel/observability/metrics.Provider.GaugeVec instead.
func BadOtelUpDownCounter(meter otelmetric.Meter) (otelmetric.Float64UpDownCounter, error) {
	return meter.Float64UpDownCounter("bad_updown")
}

// BadOtelFloat64Gauge is a direct meter.Float64Gauge call — must be flagged
// by METRICS-GAUGEVEC-FUNNEL-01 after Wave 3 (PR #593 review fix-up) extends
// the OTel ban set. Production code must route through
// kernel/observability/metrics.Provider.GaugeVec instead.
func BadOtelFloat64Gauge(meter otelmetric.Meter) (otelmetric.Float64Gauge, error) {
	return meter.Float64Gauge("bad_gauge_v2")
}

// BadPromCounter is a direct prom.NewCounter call — must be flagged by
// METRICS-GAUGEVEC-FUNNEL-01 after B2 extends the prom ban set to all
// prom.New* constructors. Production code must route through
// adapters/prometheus/internal/promwrap instead.
func BadPromCounter() prom.Counter {
	return prom.NewCounter(prom.CounterOpts{Name: "bad_counter", Help: "fixture"})
}

// BadPromCounterVec is a direct prom.NewCounterVec call — must be flagged by
// METRICS-GAUGEVEC-FUNNEL-01. Production code must route through
// adapters/prometheus/internal/promwrap instead.
func BadPromCounterVec() prom.Collector {
	return prom.NewCounterVec(prom.CounterOpts{Name: "bad_counter_vec", Help: "fixture"}, []string{"label"})
}

// BadPromHistogramVec is a direct prom.NewHistogramVec call — must be flagged
// by METRICS-GAUGEVEC-FUNNEL-01. Production code must route through
// adapters/prometheus/internal/promwrap instead.
func BadPromHistogramVec() prom.Collector {
	return prom.NewHistogramVec(prom.HistogramOpts{Name: "bad_histogram_vec", Help: "fixture"}, []string{"label"})
}

// BadOtelFloat64Counter is a direct meter.Float64Counter call — must be
// flagged by METRICS-GAUGEVEC-FUNNEL-01 after B2 extends the OTel ban set to
// all synchronous Float64*/Int64* meter methods. Production code must route
// through adapters/otel/internal/otelwrap instead.
func BadOtelFloat64Counter(meter otelmetric.Meter) (otelmetric.Float64Counter, error) {
	return meter.Float64Counter("bad_float64_counter")
}

// BadOtelFloat64Histogram is a direct meter.Float64Histogram call — must be
// flagged by METRICS-GAUGEVEC-FUNNEL-01. Production code must route through
// adapters/otel/internal/otelwrap instead.
func BadOtelFloat64Histogram(meter otelmetric.Meter) (otelmetric.Float64Histogram, error) {
	return meter.Float64Histogram("bad_float64_histogram")
}

// BadOtelInt64Counter is a direct meter.Int64Counter call — must be flagged
// by METRICS-GAUGEVEC-FUNNEL-01 (defensive ban; no production callsite yet,
// but the synchronous Int64 surface is covered by the funnel). Production code
// must route through adapters/otel/internal/otelwrap instead.
func BadOtelInt64Counter(meter otelmetric.Meter) (otelmetric.Int64Counter, error) {
	return meter.Int64Counter("bad_int64_counter")
}
