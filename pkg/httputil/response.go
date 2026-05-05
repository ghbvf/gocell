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
// when the client closes the connection before the server finishes responding.
const StatusClientClosedRequest = errcode.StatusClientClosedRequest

// WriteJSON writes v as a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: encode response", slog.Any("error", err))
	}
}

// WritePublic writes a structured error response whose message is deliberately
// selected by the framework and safe to expose.
func WritePublic(ctx context.Context, w http.ResponseWriter, kind errcode.Kind, code errcode.Code, message string) {
	status := kind.Status()
	respCode := string(code)
	if status >= http.StatusInternalServerError {
		publicCode := string(kind.PublicCode())
		if respCode != publicCode {
			logAttrs := []any{
				slog.String("code", respCode),
				slog.String("public_code", publicCode),
				slog.Int("status", status),
			}
			logAttrs = appendCorrelationAttrs(ctx, logAttrs)
			slog.Error("write public error (5xx)", logAttrs...)
		}
		respCode = publicCode
	}
	writeErrorBody(ctx, w, status, respCode, message, map[string]any{})
}

// WriteError writes err in the canonical structured error response format.
func WriteError(ctx context.Context, w http.ResponseWriter, err error) {
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		writeErrcodeError(ctx, w, "error", ecErr)
		return
	}

	// http.MaxBytesReader (used by middleware/body_limit) returns
	// *http.MaxBytesError when a request body exceeds the configured limit.
	// Generated handlers read the body via io.ReadAll(r.Body) before strict
	// JSON decode, so this error surfaces to WriteError rather than the
	// middleware. Map to 413 ErrBodyTooLarge so clients receive the canonical
	// payload-too-large response instead of a misleading 500.
	// ref: net/http MaxBytesError godoc; nginx client_max_body_size 413.
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		writeErrcodeError(ctx, w, "error", errcode.New(
			errcode.KindPayloadTooLarge,
			errcode.ErrBodyTooLarge,
			"request body too large"))
		return
	}

	logAttrs := []any{slog.Any("error", err)}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	slog.Error("unhandled error", logAttrs...)
	writeErrcodeError(ctx, w, "error", errcode.New(
		errcode.KindInternal,
		errcode.ErrInternal,
		msgInternalServerError,
		errcode.WithCategory(errcode.CategoryInfra),
	))
}

func log4xx(ctx context.Context, label string, ecErr *errcode.Error, status int) {
	if !shouldLogClientError(ctx) {
		return
	}

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

func log5xx(ctx context.Context, label string, ecErr *errcode.Error, status int, publicCode errcode.Code) {
	logAttrs := []any{
		slog.String("code", string(ecErr.Code)),
		slog.String("public_code", string(publicCode)),
		slog.Int("status", status),
	}
	if ecErr.InternalMessage != "" {
		logAttrs = append(logAttrs, slog.String("internal", ecErr.InternalMessage))
	}
	if ecErr.Cause != nil {
		logAttrs = append(logAttrs, slog.Any("cause", ecErr.Cause))
	}
	if ecErr.Kind == errcode.KindDeadlineExceeded {
		if reason := ctxcancel.ReasonFromDetails(ecErr.Details); reason != "" {
			logAttrs = append(logAttrs, slog.String("cancel_reason", reason))
		}
	}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	slog.Error(label+" (5xx)", logAttrs...)
}

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

func writeErrcodeError(ctx context.Context, w http.ResponseWriter, label string, ecErr *errcode.Error) {
	status := ecErr.Status()
	respCode := string(ecErr.Code)
	msg := ecErr.Message
	details := ecErr.Details
	if details == nil {
		details = map[string]any{}
	}

	if status == StatusClientClosedRequest {
		if reason := ctxcancel.ReasonFromDetails(details); reason != "" {
			setCancelReason(ctx, reason)
		}
	}

	switch {
	case status >= 400 && status < 500:
		log4xx(ctx, label, ecErr, status)
	case status >= 500:
		publicCode := ecErr.PublicCode()
		log5xx(ctx, label, ecErr, status, publicCode)
		respCode = string(publicCode)
		msg = public5xxMessage(status)
		details = map[string]any{}
	}

	writeErrorBody(ctx, w, status, respCode, msg, details)
}

func writeErrorBody(ctx context.Context, w http.ResponseWriter, status int, code, message string, details map[string]any) {
	errBody := map[string]any{
		"code":    code,
		"message": message,
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
	switch status {
	case http.StatusServiceUnavailable:
		return msgServiceUnavailable
	case http.StatusGatewayTimeout:
		return msgGatewayTimeout
	default:
		return msgInternalServerError
	}
}
