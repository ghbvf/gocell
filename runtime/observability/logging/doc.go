// Package logging provides a slog.Handler that enriches log records with
// trace_id, span_id, request_id, correlation_id, and cell_id from the
// request context.
// Supports JSON and text output formats.
package logging
