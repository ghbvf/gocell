// Package httputil provides shared HTTP response helpers for GoCell handlers.
package httputil

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

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

// StatusRecorder wraps http.ResponseWriter to capture the HTTP status code.
// This is shared across middleware and observability packages to avoid
// duplicate definitions.
type StatusRecorder struct {
	http.ResponseWriter
	Status int
}

// NewStatusRecorder creates a StatusRecorder with a default status of 200.
func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w, Status: http.StatusOK}
}

// WriteHeader captures the status code and delegates to the underlying writer.
func (sr *StatusRecorder) WriteHeader(code int) {
	sr.Status = code
	sr.ResponseWriter.WriteHeader(code)
}

// mapCodeToStatus maps an errcode.Code to the appropriate HTTP status code.
func mapCodeToStatus(code errcode.Code) int {
	c := string(code)
	switch {
	case strings.Contains(c, "NOT_FOUND"):
		return http.StatusNotFound
	case strings.Contains(c, "VALIDATION") || strings.Contains(c, "INVALID_INPUT"):
		return http.StatusBadRequest
	case strings.Contains(c, "UNAUTHORIZED") || strings.Contains(c, "LOGIN_FAILED") || strings.Contains(c, "REFRESH_FAILED") || strings.Contains(c, "INVALID_TOKEN") || strings.Contains(c, "TOKEN_INVALID") || strings.Contains(c, "TOKEN_EXPIRED") || strings.Contains(c, "KEY_INVALID"):
		return http.StatusUnauthorized
	case strings.Contains(c, "FORBIDDEN"):
		return http.StatusForbidden
	case strings.Contains(c, "DUPLICATE") || strings.Contains(c, "CONFLICT"):
		return http.StatusConflict
	case strings.Contains(c, "LOCKED"):
		return http.StatusForbidden
	case strings.Contains(c, "RATE_LIMITED"):
		return http.StatusTooManyRequests
	case strings.Contains(c, "TOO_LARGE"):
		return http.StatusRequestEntityTooLarge
	default:
		return http.StatusInternalServerError
	}
}
