package setup_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
)

const (
	setupStatusPath = "/api/v1/access/setup/status"
	setupAdminPath  = "/api/v1/access/setup/admin"
)

// newHandlerMux wires the slice handler onto a celltest mux via RegisterRoutes
// — same code path cell_routes.go takes in production.
func newHandlerMux(t *testing.T, h *setup.Handler) http.Handler {
	t.Helper()
	mux := celltest.NewTestMux()
	require.NoError(t, h.RegisterRoutes(mux))
	return mux
}

func newHandlerFresh(t *testing.T) http.Handler {
	t.Helper()
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	return newHandlerMux(t, setup.NewHandler(svc))
}

// --- HandleStatus ---------------------------------------------------------

func TestHandler_Status_FreshSystem_ReturnsFalse(t *testing.T) {
	h := newHandlerFresh(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, setupStatusPath, nil)

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data setup.StatusOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.False(t, body.Data.HasAdmin)
}

func TestHandler_Status_WithAdmin_ReturnsTrue(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, nil)
	h := newHandlerMux(t, setup.NewHandler(svc))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, setupStatusPath, nil)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data setup.StatusOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.True(t, body.Data.HasAdmin)
}

// --- HandleCreateAdmin ----------------------------------------------------

func TestHandler_CreateAdmin_FreshSystem_Returns201(t *testing.T) {
	h := newHandlerFresh(t)

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data setup.CreateAdminOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "root", resp.Data.Username)
	assert.Equal(t, "root@local", resp.Data.Email)
	_, idParseErr := uuid.Parse(resp.Data.ID)
	assert.NoError(t, idParseErr, "user ID must be a valid UUID")
	assert.NotEmpty(t, resp.Data.CreatedAt)
}

func TestHandler_CreateAdmin_AlreadyExists_Returns410(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, &stubWriter{})
	h := newHandlerMux(t, setup.NewHandler(svc))

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusGone, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_SETUP_ALREADY_INITIALIZED")
	assert.Contains(t, w.Body.String(), `"key":"nextAction","value":"login"`)
	// PR-A42 N4: 410 body must not leak HTTP path literals — clients resolve
	// the login endpoint via OpenAPI / contract registry, not via wire payload.
	assert.NotContains(t, w.Body.String(), "/api/")
	assert.NotContains(t, w.Body.String(), "loginEndpoint")
}

func TestHandler_CreateAdmin_MalformedJSON_Returns400(t *testing.T) {
	h := newHandlerFresh(t)

	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_CreateAdmin_UnknownField_Returns400(t *testing.T) {
	h := newHandlerFresh(t)

	body := `{"username":"u","email":"u@x","password":"p","extra":"field"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "DecodeJSONStrict rejects unknown fields")
}

func TestHandler_CreateAdmin_BlankPassword_Returns400(t *testing.T) {
	h := newHandlerFresh(t)

	body := `{"username":"root","email":"root@local","password":""}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	// The generated handler enforces minLength:8 before reaching the service,
	// so validation returns ERR_VALIDATION_FAILED rather than ERR_AUTH_IDENTITY_INVALID_INPUT.
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}

func TestHandler_CreateAdmin_FieldLengthOutOfRange_Returns400(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantErrCode string
	}{
		{
			// Generated handler enforces maxLength:128 (byte check) before service.
			name:        "username too long",
			body:        `{"username":"` + strings.Repeat("u", 129) + `","email":"root@local","password":"SecretPass!23"}`,
			wantErrCode: "ERR_VALIDATION_FAILED",
		},
		{
			// Generated handler enforces maxLength:256 (byte check) before service.
			name:        "email too long",
			body:        `{"username":"root","email":"` + strings.Repeat("e", 257) + `","password":"SecretPass!23"}`,
			wantErrCode: "ERR_VALIDATION_FAILED",
		},
		{
			// Generated handler enforces maxLength:72 (byte check) before service.
			name:        "password too long for bcrypt",
			body:        `{"username":"root","email":"root@local","password":"` + strings.Repeat("p", 73) + `"}`,
			wantErrCode: "ERR_VALIDATION_FAILED",
		},
		{
			// 8 × "界" = 8 runes — passes minLength:8 (rune-based) but fails the
			// schema pattern "^[ -~]+$" (printable ASCII only). The JSON Schema
			// validator intercepts before the service, so ERR_VALIDATION_FAILED.
			name:        "password not printable ASCII",
			body:        `{"username":"root","email":"root@local","password":"` + strings.Repeat("界", 8) + `"}`,
			wantErrCode: "ERR_VALIDATION_FAILED",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandlerFresh(t)
			req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			h.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), tc.wantErrCode)
		})
	}
}

func TestHandler_CreateAdmin_DuplicateIdentityUser_Returns409(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedIdentityUser(t, userRepo, "root", "root@local")
	svc := newService(t, userRepo, roleRepo, &stubWriter{})
	h := newHandlerMux(t, setup.NewHandler(svc))

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_AUTH_USER_DUPLICATE")
}

func seedIdentityUser(t *testing.T, userRepo *mem.UserRepository, username, email string) {
	t.Helper()
	u, err := domain.NewUser(username, email, "$2a$10$stubhashXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", time.Now())
	require.NoError(t, err)
	u.ID = "usr-existing"
	require.NoError(t, userRepo.Create(context.Background(), u))
}
