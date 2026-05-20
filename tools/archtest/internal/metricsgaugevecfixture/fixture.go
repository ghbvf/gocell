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
// GREEN controls: any call that does NOT hit the banned symbols must produce
// zero diagnostics (none present in this fixture by design).
//
// Total expected diagnostics: 3 (one prom + two otel, after Wave 3).
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
