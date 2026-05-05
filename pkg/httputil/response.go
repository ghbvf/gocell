// Package httputil provides shared HTTP response helpers for GoCell handlers.
package httputil

import (
	"bytes"
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
//
// The message argument MUST be a compile-time const literal — runtime data
// belongs in errcode.WithDetails (typed slog.Attr). MESSAGE-CONST-LITERAL-01
// archtest extends to this helper: any caller passing a runtime variable as
// message is statically rejected at the call site.
func WritePublic(ctx context.Context, w http.ResponseWriter, kind errcode.Kind, code errcode.Code, message string) {
	status := kind.Status()
	respCode := code
	if status >= http.StatusInternalServerError {
		publicCode := kind.PublicCode()
		if respCode != publicCode {
			logAttrs := []any{
				slog.String("code", string(respCode)),
				slog.String("public_code", string(publicCode)),
				slog.Int("status", status),
			}
			logAttrs = appendCorrelationAttrs(ctx, logAttrs)
			slog.Error("write public error (5xx)", logAttrs...)
		}
		respCode = publicCode
	}
	writeErrorBody(ctx, w, status, &errcode.Error{Kind: kind, Code: respCode, Message: message})
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
		if reason := ctxcancel.ReasonFromDetails(ecErr); reason != "" {
			logAttrs = append(logAttrs, slog.String("cancel_reason", reason))
		}
	}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	slog.Warn(label+" (4xx)", logAttrs...)
}

// log5xx records a 5xx response. Level depends on Kind per observability.md:
//   - KindUnavailable / KindDeadlineExceeded → Warn ("降级运行" — service or
//     dependency degraded; high-frequency probes like kubelet readyz polling
//     fall here, so spamming Error would drown signal in noise).
//   - KindInternal / anything else → Error ("影响正确性" — real fault).
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
	for _, attr := range ecErr.Details {
		logAttrs = append(logAttrs, attr)
	}
	if ecErr.Kind == errcode.KindDeadlineExceeded {
		if reason := ctxcancel.ReasonFromDetails(ecErr); reason != "" {
			logAttrs = append(logAttrs, slog.String("cancel_reason", reason))
		}
	}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	switch ecErr.Kind {
	case errcode.KindUnavailable, errcode.KindDeadlineExceeded:
		slog.Warn(label+" (5xx)", logAttrs...)
	default:
		slog.Error(label+" (5xx)", logAttrs...)
	}
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
	out := ecErr

	if status == StatusClientClosedRequest {
		if reason := ctxcancel.ReasonFromDetails(ecErr); reason != "" {
			setCancelReason(ctx, reason)
		}
	}

	switch {
	case status >= 400 && status < 500:
		log4xx(ctx, label, ecErr, status)
	case status >= 500:
		publicCode := ecErr.PublicCode()
		log5xx(ctx, label, ecErr, status, publicCode)
		// Replace internal code/message with public sentinel before serializing;
		// Error.MarshalJSON also strips Details for 5xx, so the resulting wire
		// body never carries runtime context out of the process.
		switch status {
		case http.StatusServiceUnavailable:
			out = errcode.New(ecErr.Kind, publicCode, msgServiceUnavailable)
		case http.StatusGatewayTimeout:
			out = errcode.New(ecErr.Kind, publicCode, msgGatewayTimeout)
		default:
			out = errcode.New(ecErr.Kind, publicCode, msgInternalServerError)
		}
	}

	writeErrorBody(ctx, w, status, out)
}

// sentinelInternalErrorBody is the JSON body emitted when the canonical
// errcode envelope cannot itself be marshaled. Pre-encoded so the failure
// path is allocation-free and never invokes encoding/json (which is the
// thing that just failed). Tracks the v1 wire schema:
// contracts/shared/errors/error-response-v1.schema.json.
var sentinelInternalErrorBody = []byte(
	`{"error":{"code":"ERR_INTERNAL","message":"internal server error","details":[]}}`)

// writeErrorBody serializes ecErr through Error.MarshalJSON so the wire form
// is governed by the errcode package alone (single source of truth for the
// details: array<{key,value}> shape and 5xx details strip). request_id is
// merged into the inner error object before encoding because the canonical
// error envelope places it alongside code/message/details, not in the outer
// wrapper (see contracts/shared/errors/error-response-v1.schema.json).
//
// Numeric precision: the merge step decodes ecErr's marshaled bytes back
// through json.Decoder with UseNumber() so int64/uint64 details survive
// the round-trip without being coerced to float64. The default decoder
// would silently truncate any int beyond 2^53.
//
// Fail-closed: if json.Marshal/Decode fails (e.g. a Details attr bypassed
// the kind whitelist and carries a non-marshalable Go value, or a future
// custom MarshalJSON returns a malformed payload), the response still gets
// HTTP 500 + the sentinelInternalErrorBody. There is no path that returns
// an empty 200 body. ref: net/http.Error — stdlib never returns a body
// without first writing a status; this function holds the same invariant.
func writeErrorBody(ctx context.Context, w http.ResponseWriter, status int, ecErr *errcode.Error) {
	innerJSON, err := json.Marshal(ecErr)
	if err != nil {
		slog.Error("httputil: encode errcode body; emitting sentinel 500",
			slog.Any("error", err),
			slog.Int("requested_status", status))
		writeInternalErrorSentinel(w)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(innerJSON))
	dec.UseNumber()
	var inner map[string]any
	if err := dec.Decode(&inner); err != nil {
		slog.Error("httputil: decode errcode body; emitting sentinel 500",
			slog.Any("error", err),
			slog.Int("requested_status", status))
		writeInternalErrorSentinel(w)
		return
	}
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		inner["request_id"] = reqID
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if encErr := json.NewEncoder(w).Encode(map[string]any{
		"error": inner,
	}); encErr != nil {
		slog.Error("httputil: encode error response", slog.Any("error", encErr))
	}
}

// writeInternalErrorSentinel writes a hard-coded 500 response with the
// canonical error envelope. Used as the last-resort fallback when the
// normal serialization path fails — guarantees the client always sees a
// non-empty body and a 5xx status.
func writeInternalErrorSentinel(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	if _, writeErr := w.Write(sentinelInternalErrorBody); writeErr != nil {
		slog.Error("httputil: write sentinel error body",
			slog.Any("error", writeErr))
	}
}
