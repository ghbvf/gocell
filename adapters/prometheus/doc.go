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
//   - NewMetricProvider      — registers instruments on a *prom.Registry
//   - NewHookObserver        — direct cell lifecycle observer (sync per-event)
//   - RegisterOrReuseCounter — idempotent register-or-reuse helper for bare counters
//   - NewCounter             — public passthrough wrapper for adapters outside the
//     prometheus subtree (e.g. adapters/vault) blocked by Go internal/ closure
//   - NewGauge               — public passthrough wrapper (same rationale as NewCounter)
//   - NewGaugeFunc           — public passthrough wrapper (same rationale as NewCounter)
//   - NewCounterVec          — vault-only LABELED carve-out, NOT a sanctioned path:
//     bypasses metrics.Provider label/cardinality governance; debt pending
//     removal (vault loginOutcome → metrics.Provider.CounterVec, issue #885).
//     New labeled metrics MUST use metrics.Provider — do not add callers.
//
// ref: github.com/prometheus/client_golang — Registry, CounterVec, HistogramVec.
// Adopted: isolated Registry per provider, promhttp exposition owned by caller.
package prometheus
