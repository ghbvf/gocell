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
// GREEN controls: any call that does NOT hit the banned symbols must produce
// zero diagnostics (none present in this fixture by design).
//
// Total expected diagnostics: 2 (one prom + one otel).
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
