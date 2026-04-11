package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
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

		// 503 Service Unavailable
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
			WriteDomainError(context.Background(), rec,tt.err)

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
	WriteDomainError(context.Background(), rec,errors.New("something went wrong"))

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
	WriteDomainError(context.Background(), rec,ecErr)

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
			WriteDomainError(context.Background(), rec,tt.err)

			assert.True(t, rec.Code >= 500, "expected 5xx status, got %d", rec.Code)

			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

			errObj := body["error"].(map[string]any)
			assert.Equal(t, tt.wantMsg, errObj["message"],
				"5xx response must not leak internal details")
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
			WriteDomainError(context.Background(), rec,tt.err)

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
