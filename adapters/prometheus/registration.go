package prometheus

// File-level: This file is intentionally minimal. The five public funnel
// wrappers (RegisterOrReuseCounter, NewCounter, NewCounterVec, NewGauge,
// NewGaugeFunc) were removed in issue #885 when adapters/vault migrated its
// metric construction to kernel/observability/metrics.Provider. The outer-ring
// funnel is now enforced solely by Go internal/ visibility on
// adapters/prometheus/internal/promwrap — a Hard constraint with no escape hatch.
//
// Remaining public surface: NewMetricProvider and NewHookObserver (see
// provider.go and hook_observer.go respectively).
