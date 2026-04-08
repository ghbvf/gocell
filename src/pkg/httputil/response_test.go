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

func TestMapCodeToStatus_ExplicitMapping(t *testing.T) {
	tests := []struct {
		code       errcode.Code
		wantStatus int
	}{
		// NOT_FOUND group -> 404
		{errcode.ErrMetadataNotFound, http.StatusNotFound},
		{errcode.ErrCellNotFound, http.StatusNotFound},
		{errcode.ErrSliceNotFound, http.StatusNotFound},
		{errcode.ErrContractNotFound, http.StatusNotFound},
		{errcode.ErrAssemblyNotFound, http.StatusNotFound},
		{errcode.ErrJourneyNotFound, http.StatusNotFound},
		{errcode.ErrSessionNotFound, http.StatusNotFound},
		{errcode.ErrOrderNotFound, http.StatusNotFound},
		{errcode.ErrDeviceNotFound, http.StatusNotFound},
		{errcode.ErrCommandNotFound, http.StatusNotFound},

		// Validation group -> 400
		{errcode.ErrValidationFailed, http.StatusBadRequest},
		{errcode.ErrMetadataInvalid, http.StatusBadRequest},
		{errcode.ErrLifecycleInvalid, http.StatusBadRequest},
		{errcode.ErrReferenceBroken, http.StatusBadRequest},

		// Auth group -> 401
		{errcode.ErrAuthUnauthorized, http.StatusUnauthorized},
		{errcode.ErrAuthKeyInvalid, http.StatusUnauthorized},
		{errcode.ErrAuthTokenInvalid, http.StatusUnauthorized},
		{errcode.ErrAuthTokenExpired, http.StatusUnauthorized},

		// Forbidden -> 403
		{errcode.ErrAuthForbidden, http.StatusForbidden},

		// Rate limited -> 429
		{errcode.ErrRateLimited, http.StatusTooManyRequests},

		// Body too large -> 413
		{errcode.ErrBodyTooLarge, http.StatusRequestEntityTooLarge},

		// Cell-local NOT_FOUND codes -> 404
		{"ERR_AUTH_USER_NOT_FOUND", http.StatusNotFound},
		{"ERR_CONFIG_NOT_FOUND", http.StatusNotFound},
		{"ERR_FLAG_NOT_FOUND", http.StatusNotFound},

		// Cell-local validation codes -> 400
		{"ERR_AUTH_LOGIN_INVALID_INPUT", http.StatusBadRequest},
		{"ERR_CONFIG_INVALID_INPUT", http.StatusBadRequest},

		// Cell-local auth failure codes -> 401
		{"ERR_AUTH_LOGIN_FAILED", http.StatusUnauthorized},
		{"ERR_AUTH_REFRESH_FAILED", http.StatusUnauthorized},

		// Cell-local locked -> 403
		{"ERR_AUTH_USER_LOCKED", http.StatusForbidden},

		// Cell-local duplicate -> 409
		{"ERR_AUTH_USER_DUPLICATE", http.StatusConflict},
		{"ERR_CONFIG_DUPLICATE", http.StatusConflict},

		// Codes that should fallback to 500
		{errcode.ErrInternal, http.StatusInternalServerError},
		{errcode.ErrDependencyCycle, http.StatusInternalServerError},
		{errcode.ErrBusClosed, http.StatusInternalServerError},
		{errcode.ErrAdapterPGNoTx, http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := mapCodeToStatus(tt.code)
			assert.Equal(t, tt.wantStatus, got)
		})
	}
}

func TestMapCodeToStatus_UnknownCode(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteDomainError(rec, errcode.New("ERR_TOTALLY_NEW", "test"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
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
	assert.Equal(t, map[string]any{}, errObj["details"], "canonical envelope must include empty details object")
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
			err:        errcode.New(errcode.ErrCellNotFound, "cell not found"),
			wantStatus: http.StatusNotFound,
			wantCode:   string(errcode.ErrCellNotFound),
			wantMsg:    "cell not found",
		},
		{
			name:       "validation",
			err:        errcode.New(errcode.ErrValidationFailed, "field missing"),
			wantStatus: http.StatusBadRequest,
			wantCode:   string(errcode.ErrValidationFailed),
			wantMsg:    "field missing",
		},
		{
			name:       "unauthorized",
			err:        errcode.New(errcode.ErrAuthUnauthorized, "bad creds"),
			wantStatus: http.StatusUnauthorized,
			wantCode:   string(errcode.ErrAuthUnauthorized),
			wantMsg:    "bad creds",
		},
		{
			name:       "forbidden",
			err:        errcode.New(errcode.ErrAuthForbidden, "no access"),
			wantStatus: http.StatusForbidden,
			wantCode:   string(errcode.ErrAuthForbidden),
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
			assert.Equal(t, map[string]any{}, errObj["details"], "canonical envelope must include empty details object")
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
	assert.Equal(t, map[string]any{}, errObj["details"], "canonical envelope must include empty details object")
}

func TestWriteDomainError_WithDetails(t *testing.T) {
	ecErr := errcode.WithDetails(
		errcode.New(errcode.ErrValidationFailed, "field missing"),
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
