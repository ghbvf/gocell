// Package metrics provides HTTP request instrumentation interfaces and an
// in-memory implementation for development and testing.
//
// The Collector interface abstracts metric recording so that the in-memory
// implementation can be swapped with a Prometheus adapter (adapters/prometheus)
// in production without changing application code.
//
// InMemoryCollector records request counts and cumulative latency per
// (method, path, status) tuple, and exposes them as JSON via its Handler.
// For Prometheus-compatible output in production, use the adapters/prometheus
// package instead.
//
// # Usage
//
//	mc := metrics.NewInMemoryCollector()
//
//	// Register as router middleware:
//	r.Use(metrics.Middleware(mc))
//
//	// Expose /metrics endpoint:
//	r.Handle("/metrics", mc.Handler())
//
//	// In tests, inspect collected metrics:
//	snap := mc.Snapshot()
//	count := snap.RequestCounts["GET /api/v1/sessions 200"]
//
// # Metric Key Format
//
// Keys in Snapshot maps follow the format: "{METHOD} {path} {statusCode}".
// Example: "POST /api/v1/sessions 201".
package metrics
