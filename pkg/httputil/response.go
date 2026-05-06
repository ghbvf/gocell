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
	"github.com/ghbvf/gocell/pkg/redaction"
)

const (
	msgInternalServerError = "internal server error"
	msgGatewayTimeout      = "gateway timeout"
	msgServiceUnavailable  = "service unavailable"

	headerContentType = "Content-Type"
	contentTypeJSON   = "application/json"
)

// StatusClientClosedRequest is nginx's non-standard 499 status code returned
// when the client closes the connection before the server finishes responding.
const StatusClientClosedRequest = errcode.StatusClientClosedRequest

// WriteJSON writes v as a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(headerContentType, contentTypeJSON)
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
			logAttrs = AppendCorrelationAttrs(ctx, logAttrs)
			slog.Error("write public error (5xx)", logAttrs...)
		}
		respCode = publicCode
	}
	writeErrorBody(ctx, w, status, &errcode.Error{Kind: kind, Code: respCode, Message: message})
}

// WriteErrorWithStatus writes ecErr in the canonical structured error
// response format at the *given* HTTP status, instead of deriving the
// status from errcode.Kind. The same 4xx/5xx redaction policy as WriteError
// applies (5xx replaces code/message with the public sentinel; Details are
// stripped by Error.MarshalJSON; Internal never serializes).
//
// Used by typed-response-envelope generated handlers
// (PR-V1-CONTRACT-TYPED-RESPONSE-ENVELOPE) where the status is pinned to
// the typed response struct identity (e.g. Get404ErrorResponse) rather
// than reverse-derived from errcode.Kind. CH-04 governance enforces that
// the contract.yaml-declared status matches the typed struct's status, so
// any drift between the explicit status and ecErr.Kind.Status() is caught
// at codegen time, not at runtime.
func WriteErrorWithStatus(ctx context.Context, w http.ResponseWriter, status int, ecErr *errcode.Error) {
	out := ecErr

	if status == StatusClientClosedRequest {
		if reason := ctxcancel.ReasonFromDetails(ecErr); reason != "" {
			setCancelReason(ctx, reason)
		}
	}

	switch {
	case status >= 400 && status < 500:
		log4xx(ctx, "typed", ecErr, status)
	case status >= 500:
		// Derive the public code AND normalize Kind from the typed-envelope status,
		// not from ecErr.Kind. The typed-envelope guarantees the wire status (CH-06
		// enforces the contract.yaml ↔ typed-struct bijection statically), so the wire
		// code/message/Kind must agree with that status. Kind normalization closes
		// the residual gap: even if a service mistakenly constructs Xxx503ErrorResponse
		// with a 4xx-Kind errcode body (and possibly Details), the 5xx wire body
		// renders with KindUnavailable so MarshalJSON's IsClient() check strips Details
		// as required by the v1 schema (5xx details=[]).
		publicCode := errcode.PublicCodeForStatus(status)
		log5xx(ctx, "typed", ecErr, status, publicCode)
		// Each branch passes a const-literal message identifier directly to
		// errcode.New so MESSAGE-CONST-LITERAL-01 archtest is satisfied
		// (the rule rejects var-bound msg idents to prevent runtime data
		// from leaking into errcode.Error.Message).
		switch status {
		case http.StatusServiceUnavailable:
			out = errcode.New(errcode.KindUnavailable, publicCode, msgServiceUnavailable)
		case http.StatusGatewayTimeout:
			out = errcode.New(errcode.KindDeadlineExceeded, publicCode, msgGatewayTimeout)
		case http.StatusNotImplemented:
			out = errcode.New(errcode.KindNotImplemented, publicCode, msgInternalServerError)
		default:
			out = errcode.New(errcode.KindInternal, publicCode, msgInternalServerError)
		}
	}

	writeErrorBody(ctx, w, status, out)
}

// WriteNilResponseInternal writes a 500 Internal Server Error for the
// typed-envelope nil-response fallback path: when Service.Method returns
// (nil, nil), the generated handler invokes this helper instead of letting
// the framework recover from a panic. This is the "un-declared framework
// 5xx" surface called out in ADR 202605061500-adr-typed-response-envelope.md
// D1; CH-04 governance registers it in httpHelperWritesStatuses with an empty
// status set so the resulting 500 is intentionally outside contract.yaml's
// responses[] declaration surface.
func WriteNilResponseInternal(ctx context.Context, w http.ResponseWriter) {
	WriteError(ctx, w, errcode.New(
		errcode.KindInternal,
		errcode.ErrInternal,
		"service returned nil response without error",
		errcode.WithCategory(errcode.CategoryInfra),
	))
}

// WriteEncodeFaultInternal writes a 500 Internal Server Error for the
// typed-envelope visit encode failure path: when a generated visit method
// returns a non-nil error before WriteHeader (buffer-then-commit pattern),
// the handler calls this helper to commit a 500 wire response. Same exempt
// framework 5xx surface as WriteNilResponseInternal.
func WriteEncodeFaultInternal(ctx context.Context, w http.ResponseWriter) {
	WriteError(ctx, w, errcode.New(
		errcode.KindInternal,
		errcode.ErrInternal,
		"response encode failed",
		errcode.WithCategory(errcode.CategoryInfra),
	))
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
	logAttrs = AppendCorrelationAttrs(ctx, logAttrs)
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
	logAttrs = AppendCorrelationAttrs(ctx, logAttrs)
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
		logAttrs = append(logAttrs, redaction.RedactSlogAttr(attr))
	}
	if ecErr.Kind == errcode.KindDeadlineExceeded {
		if reason := ctxcancel.ReasonFromDetails(ecErr); reason != "" {
			logAttrs = append(logAttrs, slog.String("cancel_reason", reason))
		}
	}
	logAttrs = AppendCorrelationAttrs(ctx, logAttrs)
	switch ecErr.Kind {
	case errcode.KindUnavailable, errcode.KindDeadlineExceeded:
		slog.Warn(label+" (5xx)", logAttrs...)
	default:
		slog.Error(label+" (5xx)", logAttrs...)
	}
}

// AppendCorrelationAttrs appends request_id / trace_id / span_id slog
// attributes (when present in ctx) to the supplied slice. Public so
// generated typed-response handlers and other framework error logging
// callers can reuse the canonical correlation key set without re-importing
// ctxkeys directly.
//
// The order matches the framework's existing 4xx/5xx log paths
// (request_id → trace_id → span_id) so dashboards filtering on column
// position keep working.
func AppendCorrelationAttrs(ctx context.Context, attrs []any) []any {
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
	}

	writeErrorBody(ctx, w, status, ecErr)
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
// Buffer-then-commit: the full pipeline (marshal → decode → merge → encode)
// runs into a bytes.Buffer before any header or status is written to w. Only
// when the buffer is ready does the function commit headers + status + body.
// Pipeline failure → sentinelInternalErrorBody at HTTP 500 (headers not yet
// written). ref: oapi-codegen strict-responses pattern.
//
// Numeric precision: the merge step decodes ecErr's marshaled bytes back
// through json.Decoder with UseNumber() so int64/uint64 details survive
// the round-trip without being coerced to float64. The default decoder
// would silently truncate any int beyond 2^53.
//
// Fail-closed: if any pipeline step fails, the response still gets HTTP 500 +
// sentinelInternalErrorBody. There is no path that returns an empty 200 body.
// ref: net/http.Error — stdlib never returns a body without first writing a
// status; this function holds the same invariant.
func writeErrorBody(ctx context.Context, w http.ResponseWriter, status int, ecErr *errcode.Error) {
	var buf bytes.Buffer
	if err := encodeErrorEnvelopeTo(&buf, ctx, ecErr); err != nil {
		attrs := AppendCorrelationAttrs(ctx, []any{slog.Any("error", err)})
		slog.ErrorContext(ctx, "httputil: encode error envelope", attrs...)
		w.Header().Set(headerContentType, contentTypeJSON)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(sentinelInternalErrorBody)
		return
	}
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	if _, writeErr := buf.WriteTo(w); writeErr != nil {
		slog.Error("httputil: write error response", slog.Any("error", writeErr))
	}
}

// encodeErrorEnvelopeTo writes the canonical wire envelope into out, including
// numeric-precision merge and request_id injection. Returns an error if any
// step fails — caller writes sentinelInternalErrorBody to the wire instead.
func encodeErrorEnvelopeTo(out *bytes.Buffer, ctx context.Context, ecErr *errcode.Error) error {
	innerJSON, err := json.Marshal(ecErr)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(innerJSON))
	dec.UseNumber()
	var inner map[string]any
	if err := dec.Decode(&inner); err != nil {
		return err
	}
	if reqID, ok := ctxkeys.RequestIDFrom(ctx); ok {
		inner["request_id"] = reqID
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"error": inner,
	})
}

// writeInternalErrorSentinel writes a hard-coded 500 response with the
// canonical error envelope. Used as the last-resort fallback when the
// normal serialization path fails — guarantees the client always sees a
// non-empty body and a 5xx status.
func writeInternalErrorSentinel(w http.ResponseWriter) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(http.StatusInternalServerError)
	if _, writeErr := w.Write(sentinelInternalErrorBody); writeErr != nil {
		slog.Error("httputil: write sentinel error body",
			slog.Any("error", writeErr))
	}
}
