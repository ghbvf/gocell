package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
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
		{errcode.ErrAuthUserNotFound, http.StatusNotFound},
		{errcode.ErrConfigNotFound, http.StatusNotFound},
		{errcode.ErrFlagNotFound, http.StatusNotFound},
		{errcode.ErrAuthRoleNotFound, http.StatusNotFound},
		{errcode.ErrConfigRepoNotFound, http.StatusNotFound},
		{errcode.ErrWSConnNotFound, http.StatusNotFound},
		{errcode.ErrAuditRepoNotFound, http.StatusNotFound},

		// Cell-local validation codes -> 400
		{errcode.ErrAuthLoginInvalidInput, http.StatusBadRequest},
		{errcode.ErrConfigInvalidInput, http.StatusBadRequest},
		{errcode.ErrAuthInvalidInput, http.StatusBadRequest},
		{errcode.ErrAuthIdentityInvalidInput, http.StatusBadRequest},
		{errcode.ErrAuthRefreshInvalidInput, http.StatusBadRequest},
		{errcode.ErrAuthSessionInvalidInput, http.StatusBadRequest},
		{errcode.ErrAuthLogoutInvalidInput, http.StatusBadRequest},
		{errcode.ErrAuthRBACInvalidInput, http.StatusBadRequest},
		{errcode.ErrConfigPublishInvalidInput, http.StatusBadRequest},
		{errcode.ErrFlagInvalidInput, http.StatusBadRequest},

		// Cell-local auth failure codes -> 401
		{errcode.ErrAuthLoginFailed, http.StatusUnauthorized},
		{errcode.ErrAuthRefreshFailed, http.StatusUnauthorized},
		{errcode.ErrAuthRefreshTokenReuse, http.StatusUnauthorized},
		{errcode.ErrAuthInvalidToken, http.StatusUnauthorized},

		// Cell-local locked -> 403
		{errcode.ErrAuthUserLocked, http.StatusForbidden},

		// CSRF -> 403
		{errcode.ErrCSRFOriginDenied, http.StatusForbidden},

		// Cell-local duplicate -> 409
		{errcode.ErrAuthUserDuplicate, http.StatusConflict},
		{errcode.ErrConfigDuplicate, http.StatusConflict},
		{errcode.ErrConfigRepoDuplicate, http.StatusConflict},
		{errcode.ErrFlagDuplicate, http.StatusConflict},

		// Verify/kernel codes
		{errcode.ErrCheckRefInvalid, http.StatusBadRequest},
		{errcode.ErrZeroTestMatch, http.StatusNotFound},

		// 500 Internal Server Error (explicit, not fallback)
		{errcode.ErrInternal, http.StatusInternalServerError},
		{errcode.ErrDependencyCycle, http.StatusInternalServerError},
		{errcode.ErrBusClosed, http.StatusInternalServerError},
		{errcode.ErrAdapterPGNoTx, http.StatusInternalServerError},
		{errcode.ErrTestExecution, http.StatusInternalServerError},
		{errcode.ErrCellMissingOutbox, http.StatusInternalServerError},
		{errcode.ErrCellMissingCodec, http.StatusInternalServerError},

		// 503 Service Unavailable
		{errcode.ErrCircuitOpen, http.StatusServiceUnavailable},
		{errcode.ErrWSHubStopping, http.StatusServiceUnavailable},
		{errcode.ErrWSHubNotRunning, http.StatusServiceUnavailable},
		{errcode.ErrWSMaxConns, http.StatusServiceUnavailable},

		// 501 Not Implemented
		{errcode.ErrNotImplemented, http.StatusNotImplemented},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := MapCodeToStatus(tt.code)
			assert.Equal(t, tt.wantStatus, got)
		})
	}
}

func TestMapCodeToStatus_UnknownCode(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteDomainError(context.Background(), rec, errcode.New("ERR_TOTALLY_NEW", "test"))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestWriteError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(context.Background(), rec, http.StatusBadRequest, "ERR_VALIDATION_REQUIRED_FIELD", "field is required")

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

func TestWriteError_5xx_MasksMessage(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(context.Background(), rec, http.StatusInternalServerError, "ERR_INTERNAL", "db connection pool exhausted")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "internal server error", errObj["message"],
		"WriteError must mask 5xx messages to prevent information leakage")
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
			WriteDomainError(context.Background(), rec, tt.err)

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
	WriteDomainError(context.Background(), rec, errors.New("something went wrong"))

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
	WriteDomainError(context.Background(), rec, ecErr)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	details := errObj["details"].(map[string]any)
	assert.Equal(t, "email", details["field"])
}

func TestWriteDomainError_5xx_HidesMessage(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "Safe error with InternalMessage — 5xx hides both",
			err:     errcode.Safe(errcode.ErrInternal, "something broke", "postgres pool exhausted"),
			wantMsg: "internal server error",
		},
		{
			name:    "New error — 5xx hides original Message",
			err:     errcode.New(errcode.ErrDependencyCycle, "a -> b -> a cycle detected"),
			wantMsg: "internal server error",
		},
		{
			name:    "500 with Details — still hides message",
			err:     errcode.WithDetails(errcode.New(errcode.ErrBusClosed, "bus is closed"), map[string]any{"bus": "main"}),
			wantMsg: "internal server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteDomainError(context.Background(), rec, tt.err)

			assert.True(t, rec.Code >= 500, "expected 5xx status, got %d", rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			errObj := body["error"].(map[string]any)
			assert.Equal(t, tt.wantMsg, errObj["message"],
				"5xx response must not leak internal details")
			assert.Equal(t, map[string]any{}, errObj["details"],
				"5xx response must strip details to empty object")
		})
	}
}

func TestWriteDomainError_4xx_ShowsMessage(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "400 shows original message",
			err:     errcode.New(errcode.ErrValidationFailed, "email is required"),
			wantMsg: "email is required",
		},
		{
			name:    "404 shows original message",
			err:     errcode.New(errcode.ErrCellNotFound, "cell access-core not found"),
			wantMsg: "cell access-core not found",
		},
		{
			name:    "Safe error 400 — shows public Message not InternalMessage",
			err:     errcode.Safe(errcode.ErrValidationFailed, "invalid input", "field X has regex mismatch"),
			wantMsg: "invalid input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteDomainError(context.Background(), rec, tt.err)

			assert.True(t, rec.Code >= 400 && rec.Code < 500, "expected 4xx status, got %d", rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			errObj := body["error"].(map[string]any)
			assert.Equal(t, tt.wantMsg, errObj["message"],
				"4xx response should show public message")
		})
	}
}

func TestWriteError_WithRequestID(t *testing.T) {
	ctx := ctxkeys.WithRequestID(context.Background(), "req-abc-123")
	rec := httptest.NewRecorder()
	WriteError(ctx, rec, http.StatusBadRequest, "ERR_VALIDATION_FAILED", "field is required")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "req-abc-123", errObj["request_id"],
		"response should include request_id from context")
	assert.Equal(t, "ERR_VALIDATION_FAILED", errObj["code"])
	assert.Equal(t, "field is required", errObj["message"])
}

func TestWriteError_WithoutRequestID(t *testing.T) {
	ctx := context.Background()
	rec := httptest.NewRecorder()
	WriteError(ctx, rec, http.StatusBadRequest, "ERR_VALIDATION_FAILED", "field is required")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	_, hasRequestID := errObj["request_id"]
	assert.False(t, hasRequestID,
		"response should not include request_id when not in context")
}

func TestWriteDecodeError_Contract(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "errcode ErrValidationFailed → 400",
			err:        errcode.New(errcode.ErrValidationFailed, "bad json"),
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_FAILED",
			wantMsg:    "bad json",
		},
		{
			name:       "errcode ErrBodyTooLarge → 413",
			err:        errcode.New(errcode.ErrBodyTooLarge, "payload exceeded limit"),
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   "ERR_BODY_TOO_LARGE",
			wantMsg:    "payload exceeded limit",
		},
		{
			name:       "errcode ErrInternal → 500 (message masked)",
			err:        errcode.New(errcode.ErrInternal, "db pool exhausted"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   "ERR_INTERNAL",
			wantMsg:    "internal server error",
		},
		{
			name:       "non-errcode error → 400 fallback",
			err:        errors.New("some decode error"),
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_FAILED",
			wantMsg:    "invalid request body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteDecodeError(context.Background(), rec, tt.err)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			errObj := body["error"].(map[string]any)
			assert.Equal(t, tt.wantCode, errObj["code"])
			assert.Equal(t, tt.wantMsg, errObj["message"])
		})
	}
}

func TestWriteDecodeError_Details(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantDetails map[string]any
	}{
		{
			name: "4xx with details passes through",
			err: errcode.WithDetails(
				errcode.New(errcode.ErrValidationFailed, "invalid request body"),
				map[string]any{"reason": "empty body"},
			),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "ERR_VALIDATION_FAILED",
			wantDetails: map[string]any{"reason": "empty body"},
		},
		{
			name:        "4xx without details returns empty object",
			err:         errcode.New(errcode.ErrValidationFailed, "bad json"),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "ERR_VALIDATION_FAILED",
			wantDetails: map[string]any{},
		},
		{
			name: "unknown field includes field name",
			err: errcode.WithDetails(
				errcode.New(errcode.ErrValidationFailed, "invalid request body"),
				map[string]any{"reason": "unknown field", "field": "foo"},
			),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "ERR_VALIDATION_FAILED",
			wantDetails: map[string]any{"reason": "unknown field", "field": "foo"},
		},
		{
			name: "413 with details passes through",
			err: errcode.WithDetails(
				errcode.New(errcode.ErrBodyTooLarge, "request body too large"),
				map[string]any{"maxBytes": float64(1048576)},
			),
			wantStatus:  http.StatusRequestEntityTooLarge,
			wantCode:    "ERR_BODY_TOO_LARGE",
			wantDetails: map[string]any{"maxBytes": float64(1048576)},
		},
		{
			name: "5xx details masked",
			err: errcode.WithDetails(
				errcode.New(errcode.ErrInternal, "db pool exhausted"),
				map[string]any{"host": "db-3"},
			),
			wantStatus:  http.StatusInternalServerError,
			wantCode:    "ERR_INTERNAL",
			wantDetails: map[string]any{},
		},
		{
			name:        "non-errcode error returns empty details",
			err:         errors.New("some decode error"),
			wantStatus:  http.StatusBadRequest,
			wantCode:    "ERR_VALIDATION_FAILED",
			wantDetails: map[string]any{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			WriteDecodeError(context.Background(), rec, tt.err)

			assert.Equal(t, tt.wantStatus, rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			errObj := body["error"].(map[string]any)
			assert.Equal(t, tt.wantCode, errObj["code"])

			details, ok := errObj["details"].(map[string]any)
			if !ok {
				details = map[string]any{}
			}
			assert.Equal(t, tt.wantDetails, details)
		})
	}
}

func TestWriteDecodeError_PassesCtx(t *testing.T) {
	ctx := ctxkeys.WithRequestID(context.Background(), "req-decode-456")
	rec := httptest.NewRecorder()
	WriteDecodeError(ctx, rec, errcode.New(errcode.ErrValidationFailed, "bad json"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "req-decode-456", errObj["request_id"])
}

func TestWriteDomainError_PassesCtx(t *testing.T) {
	ctx := ctxkeys.WithRequestID(context.Background(), "req-domain-789")
	rec := httptest.NewRecorder()
	WriteDomainError(ctx, rec, errcode.New(errcode.ErrCellNotFound, "cell not found"))

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "req-domain-789", errObj["request_id"])
}

func TestWriteDomainError_5xx_LogsCorrelation(t *testing.T) {
	// Exercise all ctx correlation branches in the 5xx log path.
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-5xx-001")
	ctx = ctxkeys.WithTraceID(ctx, "trace-5xx-001")
	ctx = ctxkeys.WithSpanID(ctx, "span-5xx-001")

	rec := httptest.NewRecorder()
	err := errcode.Safe(errcode.ErrInternal, "something broke", "pool exhausted on host db-3")
	WriteDomainError(ctx, rec, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "internal server error", errObj["message"])
	assert.Equal(t, "req-5xx-001", errObj["request_id"])
}

func TestWriteDomainError_5xx_WithCause(t *testing.T) {
	// Cover the ecErr.Cause branch in 5xx logging.
	inner := errors.New("connection refused")
	err := errcode.Wrap(errcode.ErrInternal, "db failed", inner)

	rec := httptest.NewRecorder()
	WriteDomainError(context.Background(), rec, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "internal server error", errObj["message"])
}

func TestWriteDomainError_PlainError_WithCorrelation(t *testing.T) {
	// Cover the non-errcode 500 path with ctx correlation.
	ctx := ctxkeys.WithRequestID(context.Background(), "req-plain-500")
	ctx = ctxkeys.WithTraceID(ctx, "trace-plain-500")

	rec := httptest.NewRecorder()
	WriteDomainError(ctx, rec, errors.New("unexpected nil pointer"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "internal server error", errObj["message"])
	assert.Equal(t, "req-plain-500", errObj["request_id"])
}

func TestWriteError_5xx_AlreadyMasked(t *testing.T) {
	// When message is already "internal server error", WriteError should not
	// double-log — just pass through.
	rec := httptest.NewRecorder()
	WriteError(context.Background(), rec, http.StatusInternalServerError, "ERR_INTERNAL", "internal server error")

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj := body["error"].(map[string]any)
	assert.Equal(t, "internal server error", errObj["message"])
}

func TestIsClientError(t *testing.T) {
	tests := []struct {
		code errcode.Code
		want bool
	}{
		// 4xx → true
		{errcode.ErrValidationFailed, true},
		{errcode.ErrCellNotFound, true},
		{errcode.ErrAuthUnauthorized, true},
		{errcode.ErrAuthForbidden, true},
		{errcode.ErrRateLimited, true},
		{errcode.ErrBodyTooLarge, true},
		{errcode.ErrAuthUserDuplicate, true},

		// 5xx → false
		{errcode.ErrInternal, false},
		{errcode.ErrDependencyCycle, false},
		{errcode.ErrBusClosed, false},

		// 503 → false
		{errcode.ErrWSHubStopping, false},

		// 501 → false
		{errcode.ErrNotImplemented, false},

		// unknown → false
		{errcode.Code("ERR_UNKNOWN_CODE"), false},
	}

	for _, tt := range tests {
		t.Run(string(tt.code), func(t *testing.T) {
			got := IsClientError(tt.code)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMapCodeToStatus_Exported(t *testing.T) {
	// Verify exported MapCodeToStatus matches internal behavior.
	assert.Equal(t, http.StatusNotFound, MapCodeToStatus(errcode.ErrCellNotFound))
	assert.Equal(t, http.StatusBadRequest, MapCodeToStatus(errcode.ErrValidationFailed))
	assert.Equal(t, http.StatusInternalServerError, MapCodeToStatus(errcode.ErrInternal))
	assert.Equal(t, http.StatusInternalServerError, MapCodeToStatus("ERR_UNKNOWN"))
}

// brokenWriter is an http.ResponseWriter whose Write always fails,
// used to exercise json.Encode error branches.
type brokenWriter struct {
	header http.Header
	code   int
}

func newBrokenWriter() *brokenWriter         { return &brokenWriter{header: http.Header{}} }
func (w *brokenWriter) Header() http.Header  { return w.header }
func (w *brokenWriter) WriteHeader(code int) { w.code = code }
func (w *brokenWriter) Write([]byte) (int, error) {
	return 0, errors.New("broken pipe")
}

func TestWriteJSON_EncodeFail(t *testing.T) {
	w := newBrokenWriter()
	// Should not panic — error is logged via slog.
	assert.NotPanics(t, func() {
		WriteJSON(w, http.StatusOK, map[string]string{"k": "v"})
	})
}

func TestWriteError_EncodeFail(t *testing.T) {
	w := newBrokenWriter()
	assert.NotPanics(t, func() {
		WriteError(context.Background(), w, http.StatusBadRequest, "ERR_TEST", "test")
	})
}

func TestWriteDomainError_EncodeFail(t *testing.T) {
	w := newBrokenWriter()
	assert.NotPanics(t, func() {
		WriteDomainError(context.Background(), w, errcode.New(errcode.ErrCellNotFound, "not found"))
	})
}

func TestWriteDecodeError_EncodeFail(t *testing.T) {
	w := newBrokenWriter()
	// errcode path → writeErrcodeError → broken encoder
	assert.NotPanics(t, func() {
		WriteDecodeError(context.Background(), w, errcode.New(errcode.ErrValidationFailed, "bad"))
	})
	// non-errcode path → WriteError → broken encoder
	w2 := newBrokenWriter()
	assert.NotPanics(t, func() {
		WriteDecodeError(context.Background(), w2, errors.New("raw error"))
	})
}

// captureHandler is an slog.Handler that captures all log records for inspection.
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(name string) slog.Handler       { return h }

// attrValue searches a slog.Record for an attribute by key and returns its string value.
func attrValue(r slog.Record, key string) (string, bool) {
	var result string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			result = a.Value.String()
			found = true
			return false
		}
		return true
	})
	return result, found
}

// findWarnRecord returns the first slog.Record at Warn level captured by h,
// or nil if none was recorded.
func findWarnRecord(h *captureHandler) *slog.Record {
	for i := range h.records {
		if h.records[i].Level == slog.LevelWarn {
			return &h.records[i]
		}
	}
	return nil
}

// assertStringAttr asserts that the named attr is present on the record and
// equals want.
func assertStringAttr(t *testing.T, rec slog.Record, key, want string) {
	t.Helper()
	got, ok := attrValue(rec, key)
	assert.True(t, ok, "log record must contain %q attr", key)
	assert.Equal(t, want, got)
}

// assertAttrAbsent asserts that the named attr is NOT present on the record.
func assertAttrAbsent(t *testing.T, rec slog.Record, key, reason string) {
	t.Helper()
	_, has := attrValue(rec, key)
	assert.False(t, has, reason)
}

// assertInternalAttr asserts either the 'internal' attr matches internalMessage
// (when non-empty) or is absent (when empty).
func assertInternalAttr(t *testing.T, rec slog.Record, internalMessage string) {
	t.Helper()
	if internalMessage != "" {
		assertStringAttr(t, rec, "internal", internalMessage)
		return
	}
	assertAttrAbsent(t, rec, "internal", "log record must NOT contain 'internal' attr when InternalMessage is empty")
}

func TestWriteDomainError_4xx_LogsWarn(t *testing.T) {
	tests := []struct {
		name            string
		code            errcode.Code
		wantStatus      int
		internalMessage string // non-empty → expect 'internal' attr in log
	}{
		{name: string(errcode.ErrValidationFailed), code: errcode.ErrValidationFailed, wantStatus: http.StatusBadRequest},
		{name: string(errcode.ErrAuthUnauthorized), code: errcode.ErrAuthUnauthorized, wantStatus: http.StatusUnauthorized},
		{name: string(errcode.ErrAuthForbidden), code: errcode.ErrAuthForbidden, wantStatus: http.StatusForbidden},
		{name: string(errcode.ErrMetadataNotFound), code: errcode.ErrMetadataNotFound, wantStatus: http.StatusNotFound},
		{name: string(errcode.ErrConfigDuplicate), code: errcode.ErrConfigDuplicate, wantStatus: http.StatusConflict},
		{name: string(errcode.ErrBodyTooLarge), code: errcode.ErrBodyTooLarge, wantStatus: http.StatusRequestEntityTooLarge},
		{name: string(errcode.ErrRateLimited), code: errcode.ErrRateLimited, wantStatus: http.StatusTooManyRequests},
		// With InternalMessage: 'internal' attr must be emitted; client 'message' still absent.
		{
			name:            "with_internal_message",
			code:            errcode.ErrAuthUnauthorized,
			wantStatus:      http.StatusUnauthorized,
			internalMessage: "subject missing from ctx — auth middleware did not run",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := &captureHandler{}
			orig := slog.Default()
			slog.SetDefault(slog.New(handler))
			t.Cleanup(func() { slog.SetDefault(orig) })

			ctx := context.Background()
			ctx = ctxkeys.WithRequestID(ctx, "req-4xx-001")
			ctx = ctxkeys.WithTraceID(ctx, "trace-4xx-001")
			ctx = ctxkeys.WithSpanID(ctx, "span-4xx-001")

			err := errcode.New(tt.code, "test message")
			if tt.internalMessage != "" {
				err = errcode.Safe(tt.code, "client-facing message", tt.internalMessage)
			}

			rec := httptest.NewRecorder()
			WriteDomainError(ctx, rec, err)
			assert.Equal(t, tt.wantStatus, rec.Code)

			warnRec := findWarnRecord(handler)
			require.NotNil(t, warnRec, "expected a slog.Warn record for 4xx response")

			assertStringAttr(t, *warnRec, "code", string(tt.code))
			assertStringAttr(t, *warnRec, "status", slog.IntValue(tt.wantStatus).String())
			// Client-facing 'message' must NOT appear in server logs — it may
			// contain user identifiers interpolated by the caller into errcode.New.
			assertAttrAbsent(t, *warnRec, "message", "log record must NOT contain 'message' attr — use InternalMessage for diagnostics")
			assertInternalAttr(t, *warnRec, tt.internalMessage)
			assertStringAttr(t, *warnRec, "request_id", "req-4xx-001")
			assertStringAttr(t, *warnRec, "trace_id", "trace-4xx-001")
			assertStringAttr(t, *warnRec, "span_id", "span-4xx-001")
		})
	}
}

// TestWriteDomainError_4xx_LogsWarn_NoCorrelation verifies that the slog.Warn
// record is emitted even when the context carries no request_id/trace_id/span_id,
// and that those attrs are absent in that case. The client-facing 'message' attr
// must also be absent (F3: log4xx omits Message to prevent identifier leakage).
func TestWriteDomainError_4xx_LogsWarn_NoCorrelation(t *testing.T) {
	handler := &captureHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(orig) })

	ctx := context.Background() // no correlation IDs
	rec := httptest.NewRecorder()
	WriteDomainError(ctx, rec, errcode.New(errcode.ErrAuthUnauthorized, "no ctx"))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	var warnRec *slog.Record
	for i := range handler.records {
		if handler.records[i].Level == slog.LevelWarn {
			warnRec = &handler.records[i]
			break
		}
	}
	require.NotNil(t, warnRec, "expected a slog.Warn record for 4xx response")

	_, hasCode := attrValue(*warnRec, "code")
	assert.True(t, hasCode, "log record must contain 'code' attr")

	_, hasStatus := attrValue(*warnRec, "status")
	assert.True(t, hasStatus, "log record must contain 'status' attr")

	// Client-facing message must NOT appear in server WARN logs.
	_, hasMsg := attrValue(*warnRec, "message")
	assert.False(t, hasMsg, "log record must NOT contain 'message' attr")

	_, hasReqID := attrValue(*warnRec, "request_id")
	assert.False(t, hasReqID, "log record must NOT contain 'request_id' attr when not set in ctx")

	_, hasTraceID := attrValue(*warnRec, "trace_id")
	assert.False(t, hasTraceID, "log record must NOT contain 'trace_id' attr when not set in ctx")

	_, hasSpanID := attrValue(*warnRec, "span_id")
	assert.False(t, hasSpanID, "log record must NOT contain 'span_id' attr when not set in ctx")
}

// TestCodeToStatus_Exhaustive parses pkg/errcode/errcode.go with go/ast,
// extracts every Code constant, and verifies it has an entry in codeToStatus.
// This fails loudly when a new errcode.Code is added without registering an
// HTTP status mapping, forcing the developer to make a conscious choice.
func TestCodeToStatus_Exhaustive(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	errcodeFile := filepath.Join(filepath.Dir(thisFile), "..", "errcode", "errcode.go")

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, errcodeFile, nil, 0)
	require.NoError(t, err, "failed to parse errcode.go")

	// Collect string values of all `const ... Code = "..."` declarations.
	var codes []string
	for _, decl := range f.Decls {
		genDecl, ok := decl.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		for _, spec := range genDecl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			ident, ok := vs.Type.(*ast.Ident)
			if !ok || ident.Name != "Code" {
				continue
			}
			for _, val := range vs.Values {
				lit, ok := val.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				codes = append(codes, s)
			}
		}
	}

	require.NotEmpty(t, codes, "should find Code constants in errcode.go")

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			_, registered := codeToStatus[errcode.Code(code)]
			assert.True(t, registered,
				"errcode.Code %q has no entry in codeToStatus — add it to the map in response.go", code)
		})
	}
}
