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
//   - NewMetricProvider — registers instruments on a *prom.Registry
//   - NewHookObserver   — direct cell lifecycle observer (sync per-event)
//
// ref: github.com/prometheus/client_golang — Registry, CounterVec, HistogramVec.
// Adopted: isolated Registry per provider, promhttp exposition owned by caller.
package prometheus
