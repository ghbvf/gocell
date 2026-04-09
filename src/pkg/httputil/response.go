// Package httputil provides shared HTTP response helpers for GoCell handlers.
package httputil

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// WriteJSON writes v as a JSON response with the given HTTP status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: encode response", slog.Any("error", err))
	}
}

// WriteError writes a structured error response in the canonical format:
//
//	{"error": {"code": "ERR_*", "message": "...", "details": {}}}
//
// Callers that need additional response headers (e.g. Retry-After) must set
// them before calling WriteError, as it calls w.WriteHeader internally.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": map[string]any{},
		},
	}); err != nil {
		slog.Error("httputil: encode error response", slog.Any("error", err))
	}
}

// WriteDomainError inspects err and writes the appropriate HTTP error response.
//   - If err is an *errcode.Error the error code is mapped to an HTTP status and
//     the errcode Message is used as the response message.
//   - Otherwise a generic 500 "internal server error" is returned and the
//     original error is logged via slog.
func WriteDomainError(w http.ResponseWriter, err error) {
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		status := mapCodeToStatus(ecErr.Code)
		details := ecErr.Details
		if details == nil {
			details = map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if encErr := json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"code":    string(ecErr.Code),
				"message": ecErr.Message,
				"details": details,
			},
		}); encErr != nil {
			slog.Error("httputil: encode domain error response", slog.Any("error", encErr))
		}
		return
	}

	// Non-errcode errors: 500 + log original error, do not expose internals.
	slog.Error("unhandled error", slog.Any("error", err))
	WriteError(w, http.StatusInternalServerError, string(errcode.ErrInternal), "internal server error")
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

	// --- 400 Bad Request ---
	errcode.ErrValidationFailed:          http.StatusBadRequest,
	errcode.ErrMetadataInvalid:           http.StatusBadRequest,
	errcode.ErrLifecycleInvalid:          http.StatusBadRequest,
	errcode.ErrReferenceBroken:           http.StatusBadRequest,
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

	// --- 401 Unauthorized ---
	errcode.ErrAuthUnauthorized:      http.StatusUnauthorized,
	errcode.ErrAuthKeyInvalid:        http.StatusUnauthorized,
	errcode.ErrAuthTokenInvalid:      http.StatusUnauthorized,
	errcode.ErrAuthTokenExpired:      http.StatusUnauthorized,
	errcode.ErrAuthLoginFailed:       http.StatusUnauthorized,
	errcode.ErrAuthRefreshFailed:     http.StatusUnauthorized,
	errcode.ErrAuthRefreshTokenReuse: http.StatusUnauthorized,
	errcode.ErrAuthInvalidToken:      http.StatusUnauthorized,

	// --- 403 Forbidden ---
	errcode.ErrAuthForbidden:  http.StatusForbidden,
	errcode.ErrAuthUserLocked: http.StatusForbidden,

	// --- 409 Conflict ---
	errcode.ErrAuthUserDuplicate:  http.StatusConflict,
	errcode.ErrConfigDuplicate:    http.StatusConflict,
	errcode.ErrConfigRepoDuplicate: http.StatusConflict,
	errcode.ErrFlagDuplicate:      http.StatusConflict,

	// --- 429 Too Many Requests ---
	errcode.ErrRateLimited: http.StatusTooManyRequests,

	// --- 413 Request Entity Too Large ---
	errcode.ErrBodyTooLarge: http.StatusRequestEntityTooLarge,

	// --- 503 Service Unavailable ---
	errcode.ErrWSHubStopping:  http.StatusServiceUnavailable,
	errcode.ErrWSHubNotRunning: http.StatusServiceUnavailable,
	errcode.ErrWSMaxConns:     http.StatusServiceUnavailable,

	// --- 500 Internal Server Error ---
	errcode.ErrInternal:          http.StatusInternalServerError,
	errcode.ErrDependencyCycle:   http.StatusInternalServerError,
	errcode.ErrBusClosed:         http.StatusInternalServerError,
	errcode.ErrAdapterPGNoTx:     http.StatusInternalServerError,
	errcode.ErrTestExecution:     http.StatusInternalServerError,
	errcode.ErrCellMissingOutbox: http.StatusInternalServerError,
	errcode.ErrArchiveUpload:     http.StatusInternalServerError,
	errcode.ErrArchiveMarshal:    http.StatusInternalServerError,
	errcode.ErrAuditRepoQuery:   http.StatusInternalServerError,
	errcode.ErrConfigRepoQuery:  http.StatusInternalServerError,
	errcode.ErrAuthKeyMissing:   http.StatusInternalServerError,
	errcode.ErrWSAlreadyStarted: http.StatusInternalServerError,
	errcode.ErrWSAlreadyStopped: http.StatusInternalServerError,

	// --- 501 Not Implemented ---
	errcode.ErrNotImplemented: http.StatusNotImplemented,
}

// mapCodeToStatus maps an errcode.Code to the appropriate HTTP status code.
// Known codes use an explicit lookup table. Unknown codes default to 500
// and emit a warning log to prompt registration.
func mapCodeToStatus(code errcode.Code) int {
	if status, ok := codeToStatus[code]; ok {
		return status
	}
	slog.Warn("unmapped error code, defaulting to 500", slog.String("code", string(code)))
	return http.StatusInternalServerError
}
