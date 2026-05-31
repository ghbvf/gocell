// Package logging provides a slog.Handler that enriches log records with
// trace_id, span_id, request_id, correlation_id, and cell_id from the
// request context. Supports JSON and text output formats.
//
// Security contract: contextHandler is the process-global sink-side redaction
// barrier. Every Handle call scrubs log messages with pkg/redaction.RedactString
// and every attr with pkg/redaction.RedactSlogAttr (key-aware mask + free-form
// regex). Pre-bound attrs (logger.With) are redacted at bind time in WithAttrs.
// Call-site redaction helpers (RedactAny, RedactSlogAttr at call sites) are
// defense-in-depth only — the handler guarantees fail-closed scrubbing regardless.
//
// Enforcement: SLOG-HANDLER-SEALED-FUNNEL-01 in tools/archtest/slog_handler_sealed_funnel_test.go.
package logging
