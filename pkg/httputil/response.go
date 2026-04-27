// Package httputil provides shared HTTP response helpers for GoCell handlers.
package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	msgInternalServerError = "internal server error"
	msgGatewayTimeout      = "gateway timeout"
	msgServiceUnavailable  = "service unavailable"
)

// StatusClientClosedRequest is nginx's non-standard 499 status code returned
// when the client closes the connection before the server finishes
// responding. Re-exported from pkg/errcode for callers that only import
// pkg/httputil (e.g. tracing middleware).
//
// ref: nginx ngx_http_request.h — NGX_HTTP_CLIENT_CLOSED_REQUEST 499
const StatusClientClosedRequest = errcode.StatusClientClosedRequest

// WriteJSON writes v as a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: encode response", slog.Any("error", err))
	}
}

// WritePublicError writes a structured error response with the given message
// verbatim, even for 5xx status codes. For 5xx responses, the code is reduced
// to a status-level public code and the original code is logged. Use this for
// framework-level errors where the message is deliberately chosen and safe to
// expose (e.g. "service unavailable" for circuit breaker 503, "gateway
// timeout" for proxy 504).
//
// Most callers should use WriteError instead, which masks 5xx messages to
// prevent accidental information leakage.
func WritePublicError(ctx context.Context, w http.ResponseWriter, status int, code, message string) {
	respCode := code
	if status >= 500 {
		respCode = string(errcode.PublicCodeForStatus(status))
		if code != respCode {
			logAttrs := []any{
				slog.String("code", code),
				slog.String("public_code", respCode),
				slog.Int("status", status),
			}
			logAttrs = appendCorrelationAttrs(ctx, logAttrs)
			slog.Error("write public error (5xx)", logAttrs...)
		}
	}

	errBody := map[string]any{
		"code":    respCode,
		"message": message,
		"details": map[string]any{},
	}
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		errBody["request_id"] = reqID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": errBody,
	}); err != nil {
		slog.Error("httputil: encode error response", slog.Any("error", err))
	}
}

// WriteError writes a structured error response in the canonical format:
//
//	{"error": {"code": "ERR_*", "message": "...", "details": {}, "request_id": "..."}}
//
// If ctx carries a request_id (via ctxkeys), it is included in the response.
// Callers that need additional response headers (e.g. Retry-After) must set
// them before calling WriteError, as it calls w.WriteHeader internally.
// For 5xx responses, message and code are reduced to status-level public
// values to prevent accidental information leakage through this low-level
// function.
func WriteError(ctx context.Context, w http.ResponseWriter, status int, code, message string) {
	msg := message
	respCode := code
	if status >= 500 {
		respCode = string(errcode.PublicCodeForStatus(status))
		if message != msgInternalServerError || code != respCode {
			slog.Error("write error (5xx)",
				slog.String("code", code),
				slog.String("public_code", respCode),
				slog.String("message", message),
			)
		}
		msg = public5xxMessage(status)
	}

	errBody := map[string]any{
		"code":    respCode,
		"message": msg,
		"details": map[string]any{},
	}
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		errBody["request_id"] = reqID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": errBody,
	}); err != nil {
		slog.Error("httputil: encode error response", slog.Any("error", err))
	}
}

// WriteDecodeError writes the HTTP error response for a DecodeJSON failure.
// It maps the errcode embedded in the error to the correct HTTP status via
// MapCodeToStatus, preserving each handler's existing external contract:
//   - ErrValidationFailed  → 400 (with details: reason, field, etc.)
//   - ErrBodyTooLarge      → 413
//   - ErrInternal          → 500 (details stripped)
//
// For 4xx responses, any details attached to the errcode.Error (e.g. "reason",
// "field") are included in the response so clients can distinguish error causes.
// For 5xx responses, details are stripped to prevent information leakage.
func WriteDecodeError(ctx context.Context, w http.ResponseWriter, err error) {
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		writeErrcodeError(ctx, w, "decode error", ecErr)
		return
	}
	WriteError(ctx, w, http.StatusBadRequest, string(errcode.ErrValidationFailed), msgInvalidRequestBody)
}

// WriteDomainError inspects err and writes the appropriate HTTP error response.
//   - If err is an *errcode.Error the error code is mapped to an HTTP status.
//     For 5xx responses the message and code are reduced to status-level public
//     values, and the original detail is logged via slog. For other statuses
//     Message is used.
//   - Otherwise a generic 500 "internal server error" is returned and the
//     original error is logged via slog.
func WriteDomainError(ctx context.Context, w http.ResponseWriter, err error) {
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		writeErrcodeError(ctx, w, "domain error", ecErr)
		return
	}

	// Non-errcode errors: 500 + log original error, do not expose internals.
	logAttrs := []any{slog.Any("error", err)}
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		logAttrs = append(logAttrs, slog.String("request_id", reqID))
	}
	if traceID, ok := ctxkeys.TraceIDFrom(ctx); ok {
		logAttrs = append(logAttrs, slog.String("trace_id", traceID))
	}
	slog.Error("unhandled error", logAttrs...)
	WriteError(ctx, w, http.StatusInternalServerError, string(errcode.ErrInternal), msgInternalServerError)
}

// log4xx emits a structured WARN record for client-error responses.
//
// 4xx WARN logs stable fields only (code/status/correlation IDs + optional
// InternalMessage). The client-facing Message is intentionally omitted to
// prevent leaking user identifiers that callers may interpolate into
// errcode.New(code, fmt.Sprintf("…%s…", userID)) — see errcode.Message vs
// InternalMessage contract: Message is supplied to response writers, while
// InternalMessage is diagnostic-only and safe for server logs.
//
// 499 logs additionally carry the cancel_reason field ("canceled" |
// "deadline_exceeded") so dashboards / SLO queries can split client-disconnect
// from server-timeout buckets at the log layer without falling back to a
// tracing query. The value is the same low-cardinality enum surfaced via the
// span attribute client.cancel.reason and is sourced from
// ctxcancel.Wrap → ecErr.Details["reason"].
func log4xx(ctx context.Context, label string, ecErr *errcode.Error, status int) {
	logAttrs := []any{
		slog.String("code", string(ecErr.Code)),
		slog.Int("status", status),
	}
	if ecErr.InternalMessage != "" {
		logAttrs = append(logAttrs, slog.String("internal", ecErr.InternalMessage))
	}
	if status == StatusClientClosedRequest {
		if reason := ctxcancel.ReasonFromDetails(ecErr.Details); reason != "" {
			logAttrs = append(logAttrs, slog.String("cancel_reason", reason))
		}
	}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	slog.Warn(label+" (4xx)", logAttrs...)
}

// log5xx emits a slog.Error record for a 5xx response, including cause and correlation IDs.
//
// 504 (ErrServerTimeout) records additionally carry the cancel_reason field
// ("deadline_exceeded") symmetric with the log4xx 499 path — dashboards can
// aggregate cancel_reason across both 499 and 504 streams to compare
// canceled-vs-deadline ratios without a per-status query split.
func log5xx(ctx context.Context, label string, ecErr *errcode.Error) {
	logAttrs := []any{
		slog.String("code", string(ecErr.Code)),
		slog.String("message", ecErr.Message),
	}
	if ecErr.InternalMessage != "" {
		logAttrs = append(logAttrs, slog.String("internal", ecErr.InternalMessage))
	}
	if ecErr.Cause != nil {
		logAttrs = append(logAttrs, slog.Any("cause", ecErr.Cause))
	}
	if ecErr.Code == errcode.ErrServerTimeout {
		if reason := ctxcancel.ReasonFromDetails(ecErr.Details); reason != "" {
			logAttrs = append(logAttrs, slog.String("cancel_reason", reason))
		}
	}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	slog.Error(label+" (5xx)", logAttrs...)
}

// appendCorrelationAttrs appends request_id, trace_id, span_id from ctx to attrs.
func appendCorrelationAttrs(ctx context.Context, attrs []any) []any {
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		attrs = append(attrs, slog.String("request_id", reqID))
	}
	if traceID, ok := ctxkeys.TraceIDFrom(ctx); ok {
		attrs = append(attrs, slog.String("trace_id", traceID))
	}
	if spanID, ok := ctxkeys.SpanIDFrom(ctx); ok {
		attrs = append(attrs, slog.String("span_id", spanID))
	}
	return attrs
}

// writeErrcodeError is the shared implementation for WriteDecodeError and
// WriteDomainError when the error is an *errcode.Error. It handles:
//   - Status mapping via MapCodeToStatus
//   - 4xx: details pass-through, original message, slog.Warn per observability.md
//   - 5xx: details stripped, message/code masked, structured logging with
//     cause/internal/request_id/trace_id/span_id per observability.md
func writeErrcodeError(ctx context.Context, w http.ResponseWriter, label string, ecErr *errcode.Error) {
	status := MapCodeToStatus(ecErr.Code)
	respCode := string(ecErr.Code)
	details := ecErr.Details
	if details == nil {
		details = map[string]any{}
	}

	// 499 reason transit: surface the reason recorded by ctxcancel.Wrap
	// (Details["reason"] = "canceled" | "deadline_exceeded") onto the
	// request-scoped cancel-reason slot so tracing middleware can stamp
	// span attribute "client.cancel.reason" with the actual cause. Stays
	// a no-op when no slot was installed (e.g. unit tests writing a 499
	// directly), preserving the legacy "context_canceled" fallback.
	if status == StatusClientClosedRequest {
		if reason := ctxcancel.ReasonFromDetails(details); reason != "" {
			setCancelReason(ctx, reason)
		}
	}

	msg := ecErr.Message
	switch {
	case status >= 400 && status < 500:
		log4xx(ctx, label, ecErr, status)
	case status >= 500:
		// Never expose internal details in 5xx responses.
		// Log structured fields per observability.md:
		// "Error 级别必须含完整 error + 关联业务字段"
		log5xx(ctx, label, ecErr)
		msg = public5xxMessage(status)
		respCode = string(errcode.PublicCodeForStatus(status))
		details = map[string]any{}
	}

	errBody := map[string]any{
		"code":    respCode,
		"message": msg,
		"details": details,
	}
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		errBody["request_id"] = reqID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"error": errBody,
	}); encErr != nil {
		slog.Error("httputil: encode error response", slog.Any("error", encErr))
	}
}

func public5xxMessage(status int) string {
	if status == http.StatusServiceUnavailable {
		return msgServiceUnavailable
	}
	if status == http.StatusGatewayTimeout {
		return msgGatewayTimeout
	}
	return msgInternalServerError
}

// MapCodeToStatus maps an errcode.Code to the appropriate HTTP status code.
// Delegates to errcode.MapCodeToStatus; re-exported here so callers that
// already import pkg/httputil do not need a separate pkg/errcode import.
func MapCodeToStatus(code errcode.Code) int {
	return errcode.MapCodeToStatus(code)
}

// IsClientError returns true if the given error code maps to a 4xx HTTP status
// (client error). Delegates to errcode.IsClientError.
func IsClientError(code errcode.Code) bool {
	return errcode.IsClientError(code)
}
