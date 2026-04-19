// Package httputil provides shared HTTP response helpers for GoCell handlers.
package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const msgInternalServerError = "internal server error"

// WriteJSON writes v as a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: encode response", slog.Any("error", err))
	}
}

// WritePublicError writes a structured error response with the given message
// verbatim, even for 5xx status codes. Use this for framework-level errors
// where the message is deliberately chosen and safe to expose (e.g. "service
// unavailable" for circuit breaker 503, "gateway timeout" for proxy 504).
//
// Most callers should use WriteError instead, which masks 5xx messages to
// prevent accidental information leakage.
func WritePublicError(ctx context.Context, w http.ResponseWriter, status int, code, message string) {
	errBody := map[string]any{
		"code":    code,
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
// For 5xx responses, message is forced to "internal server error" to prevent
// accidental information leakage through this low-level function.
func WriteError(ctx context.Context, w http.ResponseWriter, status int, code, message string) {
	msg := message
	if status >= 500 && message != msgInternalServerError {
		slog.Error("write error (5xx)",
			slog.String("code", code),
			slog.String("message", message),
		)
		msg = msgInternalServerError
	}

	errBody := map[string]any{
		"code":    code,
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
//     For 5xx responses the message is always "internal server error" and the
//     original detail is logged via slog. For other statuses Message is used.
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
func log4xx(ctx context.Context, label string, ecErr *errcode.Error, status int) {
	logAttrs := []any{
		slog.String("code", string(ecErr.Code)),
		slog.Int("status", status),
	}
	if ecErr.InternalMessage != "" {
		logAttrs = append(logAttrs, slog.String("internal", ecErr.InternalMessage))
	}
	logAttrs = appendCorrelationAttrs(ctx, logAttrs)
	slog.Warn(label+" (4xx)", logAttrs...)
}

// log5xx emits a slog.Error record for a 5xx response, including cause and correlation IDs.
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
//   - 5xx: details stripped, message masked, structured logging with
//     cause/internal/request_id/trace_id/span_id per observability.md
func writeErrcodeError(ctx context.Context, w http.ResponseWriter, label string, ecErr *errcode.Error) {
	status := MapCodeToStatus(ecErr.Code)
	details := ecErr.Details
	if details == nil {
		details = map[string]any{}
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
		msg = msgInternalServerError
		details = map[string]any{}
	}

	errBody := map[string]any{
		"code":    string(ecErr.Code),
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

// codeToStatus maps known error codes to HTTP status codes.
// All codes use errcode constants for compile-time checking.
var codeToStatus = map[errcode.Code]int{
	// --- 404 Not Found ---
	errcode.ErrMetadataNotFound:   http.StatusNotFound,
	errcode.ErrCellNotFound:       http.StatusNotFound,
	errcode.ErrSliceNotFound:      http.StatusNotFound,
	errcode.ErrContractNotFound:   http.StatusNotFound,
	errcode.ErrAssemblyNotFound:   http.StatusNotFound,
	errcode.ErrJourneyNotFound:    http.StatusNotFound,
	errcode.ErrSessionNotFound:    http.StatusNotFound,
	errcode.ErrSessionConflict:    http.StatusConflict,
	errcode.ErrOrderNotFound:      http.StatusNotFound,
	errcode.ErrDeviceNotFound:     http.StatusNotFound,
	errcode.ErrCommandNotFound:    http.StatusNotFound,
	errcode.ErrAuthUserNotFound:   http.StatusNotFound,
	errcode.ErrAuthRoleNotFound:   http.StatusNotFound,
	errcode.ErrConfigNotFound:     http.StatusNotFound,
	errcode.ErrConfigRepoNotFound: http.StatusNotFound,
	errcode.ErrFlagNotFound:       http.StatusNotFound,
	errcode.ErrWSConnNotFound:     http.StatusNotFound,
	errcode.ErrAuditRepoNotFound:  http.StatusNotFound,
	errcode.ErrZeroTestMatch:      http.StatusNotFound,

	// --- 400 Bad Request ---
	errcode.ErrEnvelopeSchema:            http.StatusBadRequest,
	errcode.ErrCursorInvalid:             http.StatusBadRequest,
	errcode.ErrPageSizeExceeded:          http.StatusBadRequest,
	errcode.ErrValidationFailed:          http.StatusBadRequest,
	errcode.ErrMetadataInvalid:           http.StatusBadRequest,
	errcode.ErrLifecycleInvalid:          http.StatusBadRequest,
	errcode.ErrReferenceBroken:           http.StatusBadRequest,
	errcode.ErrCheckRefInvalid:           http.StatusBadRequest,
	errcode.ErrAuthInvalidInput:          http.StatusBadRequest,
	errcode.ErrAuthIdentityInvalidInput:  http.StatusBadRequest,
	errcode.ErrAuthLoginInvalidInput:     http.StatusBadRequest,
	errcode.ErrAuthRefreshInvalidInput:   http.StatusBadRequest,
	errcode.ErrAuthSessionInvalidInput:   http.StatusBadRequest,
	errcode.ErrAuthLogoutInvalidInput:    http.StatusBadRequest,
	errcode.ErrAuthRBACInvalidInput:      http.StatusBadRequest,
	errcode.ErrConfigInvalidInput:        http.StatusBadRequest,
	errcode.ErrConfigPublishInvalidInput: http.StatusBadRequest,
	errcode.ErrFlagInvalidInput:          http.StatusBadRequest,
	errcode.ErrInvalidTimeFormat:         http.StatusBadRequest,

	// --- 401 Unauthorized ---
	errcode.ErrAuthUnauthorized:       http.StatusUnauthorized,
	errcode.ErrAuthKeyInvalid:         http.StatusUnauthorized,
	errcode.ErrAuthVerifierConfig:     http.StatusInternalServerError,
	errcode.ErrAuthTokenInvalid:       http.StatusUnauthorized,
	errcode.ErrAuthTokenExpired:       http.StatusUnauthorized,
	errcode.ErrAuthLoginFailed:        http.StatusUnauthorized,
	errcode.ErrAuthRefreshFailed:      http.StatusUnauthorized,
	errcode.ErrAuthRefreshTokenReuse:  http.StatusUnauthorized, //nolint:staticcheck // retained for sessionrefresh.service; removed in F2 migration PR
	errcode.ErrAuthInvalidToken:       http.StatusUnauthorized,
	errcode.ErrAuthInvalidTokenIntent: http.StatusUnauthorized,
	errcode.ErrRefreshTokenNotFound:   http.StatusUnauthorized,
	errcode.ErrRefreshTokenExpired:    http.StatusUnauthorized,
	errcode.ErrRefreshTokenRevoked:    http.StatusUnauthorized,
	errcode.ErrRefreshTokenReused:     http.StatusUnauthorized,

	// --- 403 Forbidden ---
	errcode.ErrAuthForbidden:             http.StatusForbidden,
	errcode.ErrAuthUserLocked:            http.StatusForbidden,
	errcode.ErrCSRFOriginDenied:          http.StatusForbidden,
	errcode.ErrAuthPasswordResetRequired: http.StatusForbidden,

	// --- 409 Conflict ---
	errcode.ErrAuthUserDuplicate:   http.StatusConflict,
	errcode.ErrAuthSelfDelete:      http.StatusConflict,
	errcode.ErrAuthRoleDuplicate:   http.StatusConflict,
	errcode.ErrConfigDuplicate:     http.StatusConflict,
	errcode.ErrConfigRepoDuplicate: http.StatusConflict,
	errcode.ErrFlagDuplicate:       http.StatusConflict,

	// --- 429 Too Many Requests ---
	errcode.ErrRateLimited: http.StatusTooManyRequests,

	// --- 413 Request Entity Too Large ---
	errcode.ErrBodyTooLarge: http.StatusRequestEntityTooLarge,

	// --- 503 Service Unavailable ---
	errcode.ErrCircuitOpen:          http.StatusServiceUnavailable,
	errcode.ErrWSHubStopping:        http.StatusServiceUnavailable,
	errcode.ErrWSHubNotRunning:      http.StatusServiceUnavailable,
	errcode.ErrWSMaxConns:           http.StatusServiceUnavailable,
	errcode.ErrRelayBudgetExhausted: http.StatusServiceUnavailable,

	// --- 500 Internal Server Error ---
	errcode.ErrInternal:               http.StatusInternalServerError,
	errcode.ErrDependencyCycle:        http.StatusInternalServerError,
	errcode.ErrBusClosed:              http.StatusInternalServerError,
	errcode.ErrAdapterPGNoTx:          http.StatusInternalServerError,
	errcode.ErrTestExecution:          http.StatusInternalServerError,
	errcode.ErrCellMissingOutbox:      http.StatusInternalServerError,
	errcode.ErrCellMissingCodec:       http.StatusInternalServerError,
	errcode.ErrCellMissingTokenIssuer: http.StatusInternalServerError,
	errcode.ErrCellInvalidConfig:      http.StatusInternalServerError,
	errcode.ErrArchiveUpload:          http.StatusInternalServerError,
	errcode.ErrArchiveMarshal:         http.StatusInternalServerError,
	errcode.ErrAuditRepoQuery:         http.StatusInternalServerError,
	errcode.ErrConfigRepoQuery:        http.StatusInternalServerError,
	errcode.ErrAuthKeyMissing:         http.StatusInternalServerError,
	errcode.ErrWSAlreadyStarted:       http.StatusInternalServerError,
	errcode.ErrWSAlreadyStopped:       http.StatusInternalServerError,
	// Observability init failures (missing Provider, missing CellID) —
	// these originate from composition-root misconfiguration and never
	// escape via HTTP in practice, but the exhaustive test requires every
	// errcode.Code to map. 500 is the conservative choice: if one ever
	// reaches the HTTP layer, it signals an internal setup bug.
	errcode.ErrObservabilityConfigInvalid: http.StatusInternalServerError,
	// Lifecycle operation called in wrong state (e.g. bootstrap phase violation).
	errcode.ErrBootstrapLifecycle: http.StatusInternalServerError,

	// --- 501 Not Implemented ---
	errcode.ErrNotImplemented: http.StatusNotImplemented,
}

// MapCodeToStatus maps an errcode.Code to the appropriate HTTP status code.
// Known codes use an explicit lookup table. Unknown codes default to 500
// and emit a warning log to prompt registration.
func MapCodeToStatus(code errcode.Code) int {
	if status, ok := codeToStatus[code]; ok {
		return status
	}
	slog.Warn("unmapped error code, defaulting to 500", slog.String("code", string(code)))
	return http.StatusInternalServerError
}

// IsClientError returns true if the given error code maps to a 4xx HTTP status
// (client error). Unknown codes return false.
func IsClientError(code errcode.Code) bool {
	status, ok := codeToStatus[code]
	return ok && status >= 400 && status < 500
}
