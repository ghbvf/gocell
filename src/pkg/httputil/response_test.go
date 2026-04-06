package httputil

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapCodeToStatus(t *testing.T) {
	tests := []struct {
		code   errcode.Code
		want   int
	}{
		{"ERR_NOT_FOUND", http.StatusNotFound},
		{"ERR_USER_NOT_FOUND", http.StatusNotFound},
		{"ERR_VALIDATION_REQUIRED_FIELD", http.StatusBadRequest},
		{"ERR_AUTH_LOGIN_INVALID_INPUT", http.StatusBadRequest},
		{"ERR_AUTH_UNAUTHORIZED", http.StatusUnauthorized},
		{"ERR_AUTH_INVALID_TOKEN", http.StatusUnauthorized},
		{"ERR_AUTH_LOGIN_FAILED", http.StatusUnauthorized},
		{"ERR_AUTH_REFRESH_FAILED", http.StatusUnauthorized},
		{"ERR_AUTH_TOKEN_EXPIRED", http.StatusUnauthorized},
		{"ERR_AUTH_KEY_INVALID", http.StatusUnauthorized},
		{"ERR_AUTH_FORBIDDEN", http.StatusForbidden},
		{"ERR_USER_LOCKED", http.StatusForbidden},
		{"ERR_DUPLICATE_USER", http.StatusConflict},
		{"ERR_CONFLICT", http.StatusConflict},
		{"ERR_RATE_LIMITED", http.StatusTooManyRequests},
		{"ERR_TOO_LARGE", http.StatusRequestEntityTooLarge},
		{"ERR_INTERNAL", http.StatusInternalServerError},
		{"ERR_UNKNOWN_CODE", http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := mapCodeToStatus(tt.code)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, http.StatusBadRequest, "ERR_VALIDATION_REQUIRED_FIELD", "field is required")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "response must contain 'error' key")
	assert.Equal(t, "ERR_VALIDATION_REQUIRED_FIELD", errObj["code"])
	assert.Equal(t, "field is required", errObj["message"])
	assert.NotNil(t, errObj["details"], "details must be present")
}

func TestWriteJSON(t *testing.T) {
	payload := map[string]string{"hello": "world"}
	rec := httptest.NewRecorder()
	WriteJSON(rec, http.StatusCreated, payload)

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "world", body["hello"])
}

func TestWriteDomainError_ErrcodeError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "not found",
			err:        errcode.New("ERR_USER_NOT_FOUND", "user not found"),
			wantStatus: http.StatusNotFound,
			wantCode:   "ERR_USER_NOT_FOUND",
			wantMsg:    "user not found",
		},
		{
			name:       "validation",
			err:        errcode.New("ERR_VALIDATION_REQUIRED_FIELD", "field missing"),
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_REQUIRED_FIELD",
			wantMsg:    "field missing",
		},
		{
			name:       "unauthorized",
			err:        errcode.New("ERR_AUTH_UNAUTHORIZED", "bad creds"),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "ERR_AUTH_UNAUTHORIZED",
			wantMsg:    "bad creds",
		},
		{
			name:       "forbidden",
			err:        errcode.New("ERR_AUTH_FORBIDDEN", "no access"),
			wantStatus: http.StatusForbidden,
			wantCode:   "ERR_AUTH_FORBIDDEN",
			wantMsg:    "no access",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteDomainError(rec, tt.err)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			errObj, ok := body["error"].(map[string]any)
			require.True(t, ok)
			assert.Equal(t, tt.wantCode, errObj["code"])
			assert.Equal(t, tt.wantMsg, errObj["message"])
			assert.NotNil(t, errObj["details"])
		})
	}
}

func TestWriteDomainError_PlainError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteDomainError(rec, errors.New("something went wrong"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ERR_INTERNAL", errObj["code"])
	assert.Equal(t, "internal server error", errObj["message"])
}

func TestStatusRecorder(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := NewStatusRecorder(rec)

	// Default status should be 200.
	assert.Equal(t, http.StatusOK, sr.Status)

	// WriteHeader should capture the status.
	sr.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, sr.Status)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWriteDomainError_WithDetails(t *testing.T) {
	ecErr := errcode.WithDetails(
		errcode.New("ERR_VALIDATION_REQUIRED_FIELD", "field missing"),
		map[string]any{"field": "email"},
	)

	rec := httptest.NewRecorder()
	WriteDomainError(rec, ecErr)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	details := errObj["details"].(map[string]any)
	assert.Equal(t, "email", details["field"])
}
