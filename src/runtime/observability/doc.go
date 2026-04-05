// Package observability provides logging, metrics, and tracing infrastructure
// for the GoCell runtime.
//
// # Logging
//
// All logging uses log/slog with structured fields. Log levels follow the
// observability rules:
//
//   - Error: correctness impact (DB write failure, ACK failure, security events)
//   - Warn:  degraded operation (Redis unavailable, noop publisher, retry budget)
//   - Info:  lifecycle events (startup, consumer group join, migration complete)
//   - Debug: development diagnostics (disabled in production)
//
// Error-level logs must include the full error and correlated business fields
// (execution_id, policy_id, etc.). Bare slog.Error("failed") is prohibited.
//
// # Metrics
//
// Prometheus-compatible metrics are exposed at /metrics. Standard metrics
// include request duration, error rate, queue depth, and outbox lag.
//
// # Tracing
//
// OpenTelemetry-compatible distributed tracing. Trace and span IDs are
// propagated via pkg/ctxkeys context keys.
package observability
