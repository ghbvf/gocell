// Package tracing provides a Tracer interface and HTTP middleware for
// distributed tracing. The default implementation generates trace/span IDs
// and propagates them via context. Production deployments should use an
// adapter (e.g., adapters/otel) that implements the Tracer interface.
package tracing
