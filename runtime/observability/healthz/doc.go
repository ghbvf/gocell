// Package healthz provides the default in-memory [kernel/healthz.Aggregator]
// implementation for GoCell.
//
// # Architecture
//
// This package sits in runtime/observability/ — one layer above kernel/ — so
// it can import kernel/healthz (interfaces), kernel/cell (ErrDegraded), and
// kernel/clock (Clock / MustHaveClock) without violating the layering rules
// (runtime/ may import kernel/; must not import cells/, adapters/).
//
// The HTTP exposure of probe results lives in runtime/http/health, which
// consumes a [kernel/healthz.Aggregator] supplied by this package (or any
// other conforming implementation). This package MUST NOT import
// runtime/http/health.
//
// # Usage
//
//	agg := healthz.NewAggregator()
//	agg.Register(healthz.NewProbe("postgres_ready", pool.Ping))
//	snap := agg.Evaluate(ctx)
//
// # Conformance harness
//
// Any alternative Aggregator implementation (e.g. postgres-persisted,
// otel-emitting) should call [RunAggregatorConformance] to verify it satisfies
// the kernel contract. See conformance.go.
//
// # Layering constraints
//
// Allowed imports: stdlib, pkg/*, kernel/*.
// Forbidden imports: runtime/http/health, cells/*, adapters/*.
//
// ref: kubernetes/kubernetes staging/src/k8s.io/apiserver/pkg/server/healthz — Probe interface + background-ctx deadline
// ref: heptiolabs/healthcheck — flat list + AND aggregation pattern
// ref: spring-projects/spring-boot CompositeHealthContributor — tri-state severity
package healthz
