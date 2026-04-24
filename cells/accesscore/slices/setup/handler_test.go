package setup_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
)

func newHandlerFresh(t *testing.T) *setup.Handler {
	t.Helper()
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	return setup.NewHandler(svc)
}

// --- HandleStatus ---------------------------------------------------------

func TestHandler_Status_FreshSystem_ReturnsFalse(t *testing.T) {
	h := newHandlerFresh(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)

	h.HandleStatus(w, req)

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
	h := setup.NewHandler(svc)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/setup/status", nil)
	h.HandleStatus(w, req)

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
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreateAdmin(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)
	var resp struct {
		Data setup.CreateAdminOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "root", resp.Data.Username)
	assert.Equal(t, "root@local", resp.Data.Email)
	assert.Contains(t, resp.Data.ID, "usr-")
	assert.NotEmpty(t, resp.Data.CreatedAt)
}

func TestHandler_CreateAdmin_AlreadyExists_Returns409(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, &stubWriter{})
	h := setup.NewHandler(svc)

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreateAdmin(w, req)

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_SETUP_ALREADY_INITIALIZED")
}

func TestHandler_CreateAdmin_MalformedJSON_Returns400(t *testing.T) {
	h := newHandlerFresh(t)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", strings.NewReader(`not-json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreateAdmin(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_CreateAdmin_UnknownField_Returns400(t *testing.T) {
	h := newHandlerFresh(t)

	body := `{"username":"u","email":"u@x","password":"p","extra":"field"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreateAdmin(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code, "DecodeJSONStrict rejects unknown fields")
}

func TestHandler_CreateAdmin_BlankPassword_Returns400(t *testing.T) {
	h := newHandlerFresh(t)

	body := `{"username":"root","email":"root@local","password":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/setup/admin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleCreateAdmin(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_AUTH_IDENTITY_INVALID_INPUT")
}
