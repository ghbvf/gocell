// Package logging provides a slog.Handler that enriches log records with
// GoCell context values: trace_id, span_id, request_id, and cell_id.
//
// The handler wraps either a JSON or text slog handler and extracts values
// inserted by runtime/http/middleware (RequestID) and runtime/observability/tracing
// (Tracer). This ensures every log line can be correlated with an HTTP request
// and distributed trace without manual field injection in application code.
//
// # Usage
//
//	handler := logging.NewHandler(logging.Options{
//	    Level:  slog.LevelInfo,
//	    Format: logging.FormatJSON,
//	})
//	slog.SetDefault(slog.New(handler))
//
//	// All subsequent slog calls automatically include trace_id, span_id,
//	// request_id, and cell_id from the active context.
//
// # Log Level Guidelines
//
// Follow the project observability rules (.claude/rules/gocell/observability.md):
//   - Error: DB write failure, ACK failure, state-machine violation, security events
//   - Warn:  degraded operation — Redis unavailable, noop publisher, retry budget exhausted
//   - Info:  lifecycle events — service start, consumer group join, migration complete
//   - Debug: dev diagnostics only; disabled in production
package logging
