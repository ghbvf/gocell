// Package observability is the namespace root for GoCell's
// provider-neutral metrics and pool-stats abstractions. The root
// package itself declares no symbols; consumers always import a
// sub-package.
//
// Tracing abstractions (Tracer / Span / Attr) live in kernel/wrapper,
// not under this tree, because tracing is consumed by wrapper-level
// middleware that pre-dates this namespace. runtime/observability/tracing
// re-exports them as type aliases for HTTP-side callers.
//
// Layer split — what lives where:
//
//   - kernel/observability/...    — provider-neutral interfaces only.
//     Counter / Histogram / Provider (metrics), Snapshot / Statter
//     (poolstats). No backend types (prometheus, otel, …) appear in
//     these signatures, so kernel modules and cells can emit telemetry
//     without taking a transitive dependency on any exporter.
//
//   - runtime/observability/...   — wiring + in-memory implementations.
//     HTTP middleware for logging context enrichment and the in-memory
//     request collector; a stdlib-only Tracer stub re-exporting the
//     kernel/wrapper interfaces; ProviderCollector adapters that bridge
//     kernel metrics interfaces to a chosen Provider at composition
//     time. No external propagation logic — that lives in adapters.
//
//   - adapters/{prometheus,otel,…} — concrete exporters. prometheus
//     implements kernel/observability/metrics.Provider; otel implements
//     both kernel/wrapper.Tracer (with W3C TraceContext propagation)
//     and a metrics Provider, plus pool/messaging collectors. Kernel
//     and cells never import them directly; the composition root
//     (cmd/) selects one and threads it through
//     bootstrap.WithMetricsProvider, which auto-wires an HTTP
//     ProviderCollector into the router.
//
// The boundary is one-directional: adapters depend on kernel; kernel
// does not know about adapters. A NopProvider in
// kernel/observability/metrics keeps unit tests and dev wire-ups
// runnable without a backend, while still validating label-set
// contracts so drift surfaces early.
//
// ref: docs/architecture/202604252235-001-metrics-provider-abstraction-in-kernel.md
// ref: open-telemetry/opentelemetry-go metric/meter.go — API/SDK split
// that motivates this layering.
package observability
