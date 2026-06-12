// Package prometheus provides a Prometheus backend for the provider-neutral
// metrics abstraction defined in kernel/observability/metrics, plus a direct
// cell.LifecycleHookObserver implementation for assembly hook metrics.
//
// Use MetricProvider to register counters/histograms that flow through
// runtime/observability/metrics (HTTP collector) and kernel/outbox (relay
// collector). Use HookObserver for lifecycle hook metrics on an assembly.
//
// Exposed surface:
//
//   - NewMetricProvider — registers instruments on a *prom.Registry and
//     returns a kernel/observability/metrics.Provider implementation. All
//     metric construction for callers outside this package must route through
//     this factory (or HookObserver for lifecycle events).
//   - NewHookObserver — direct cell lifecycle observer (sync per-event).
//
// The five public passthrough wrappers (RegisterOrReuseCounter, NewCounter,
// NewCounterVec, NewGauge, NewGaugeFunc) were removed in issue #885 when
// adapters/vault migrated to kernel/observability/metrics.Provider. The funnel
// is now inner-ring only: adapters/prometheus/internal/promwrap is the sole
// Prometheus construction site, protected by Go internal/ visibility (Hard
// constraint — no outer-ring escape hatch remains).
//
// ref: github.com/prometheus/client_golang — Registry, CounterVec, HistogramVec.
// Adopted: isolated Registry per provider, promhttp exposition owned by caller.
package prometheus
