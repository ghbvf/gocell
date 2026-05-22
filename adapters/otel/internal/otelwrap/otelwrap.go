// Package otelwrap is the sole sanctioned funnel for invoking OpenTelemetry
// metric.Meter instrument constructors. Production code outside this package
// MUST NEVER call meter.Float64* / meter.Int64* directly.
//
// INVARIANT: METRICS-GAUGEVEC-UPSTREAM-HARD-01
//   - upstream Hard: Go internal/ package closure prevents external imports
//     at compile time; only adapters/otel/* subtree can import.
//   - downstream Hard: archtest METRICS-GAUGEVEC-FUNNEL-01 enforces method
//     form-uniqueness on otelmetric.Meter.Float64* via *types.Info resolution.
//
// ref: opentelemetry-go sdk/metric internal/aggregate (internal/ funneling idiom)
package otelwrap

import otelmetric "go.opentelemetry.io/otel/metric"

// Float64Counter is the sole sanctioned entry for meter.Float64Counter.
func Float64Counter(m otelmetric.Meter, name string, opts ...otelmetric.Float64CounterOption) (otelmetric.Float64Counter, error) {
	return m.Float64Counter(name, opts...)
}

// Float64Gauge is the sole sanctioned entry for meter.Float64Gauge.
func Float64Gauge(m otelmetric.Meter, name string, opts ...otelmetric.Float64GaugeOption) (otelmetric.Float64Gauge, error) {
	return m.Float64Gauge(name, opts...)
}

// Float64Histogram is the sole sanctioned entry for meter.Float64Histogram.
func Float64Histogram(m otelmetric.Meter, name string, opts ...otelmetric.Float64HistogramOption) (otelmetric.Float64Histogram, error) {
	return m.Float64Histogram(name, opts...)
}
