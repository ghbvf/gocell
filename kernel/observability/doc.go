// Package observability is the namespace root for GoCell's
// provider-neutral observability abstractions. The root package itself
// declares no symbols; consumers always import a sub-package.
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
//     HTTP middleware (logging context enrichment, request collector,
//     tracing propagation) and ProviderCollector adapters that bridge
//     kernel interfaces to a chosen Provider at composition time.
//
//   - adapters/{prometheus,otel,…} — concrete exporters. These import
//     kernel/observability/metrics and implement Provider against a
//     real registry. Kernel and cells never import them directly; the
//     composition root (cmd/) selects one and threads it through
//     bootstrap.WithMetricsProvider.
//
// The boundary is one-directional: adapters depend on kernel; kernel
// does not know about adapters. A NopProvider in the kernel package
// keeps unit tests and dev wire-ups runnable without a backend, while
// still validating label-set contracts so drift surfaces early.
//
// ref: docs/architecture/202604252235-001-metrics-provider-abstraction-in-kernel.md
// ref: opentelemetry-go metric/meter.go — API/SDK split that motivates
// this layering.
package observability
