package bootstrap

// options_metrics.go — With* option functions covering the metrics provider
// and HTTP collector wiring.
//
// Covers: WithMetricsProvider.
//
// ref: opentelemetry-go otel.GetMeterProvider@main — single global
// provider entry point; GoCell exposes it per-Bootstrap instance to avoid
// mutable global state.

import (
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// WithMetricsProvider registers a provider-neutral metrics backend used by
// components that need to emit counters/histograms through a common
// abstraction (hook dispatcher drop counters, OTel pool-stats collector,
// custom caller-registered metrics). Pass nil or omit the option to use
// kernel/observability/metrics.NopProvider (no emission).
//
// Callers can read b.MetricsProvider() to register additional metrics
// against the same backend — useful when cmd/* builds both an HTTP
// Collector and a relay Collector on the same Provider instance.
//
// ref: opentelemetry-go otel.GetMeterProvider@main — single global
// provider entry point; GoCell exposes it per-Bootstrap instance to avoid
// mutable global state.
func WithMetricsProvider(p kernelmetrics.Provider) Option {
	return func(b *Bootstrap) {
		if p != nil {
			b.metricsProvider = p
		}
	}
}
