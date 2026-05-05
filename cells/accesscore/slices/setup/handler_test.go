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
	"github.com/ghbvf/gocell/runtime/auth"
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

// newHandlerWithBootstrapCreds creates a handler mux whose admin endpoint is
// protected by the real bootstrap middleware (runtime/auth.NewBootstrapMiddleware).
// Both NoCreds and WrongUsername/Password tests use this helper; the production
// wiring in cell_init.go follows the same WithAdminMiddleware + bootstrap
// middleware path.
func newHandlerWithBootstrapCreds(t *testing.T, svc *setup.Service, envUsername, envPassword string) http.Handler {
	t.Helper()
	creds := auth.BootstrapCredentials{
		Username: []byte(envUsername),
		Password: []byte(envPassword),
	}
	mw := auth.NewBootstrapMiddleware(creds, auth.BootstrapAllowAllLimiter{}, nil)
	h := setup.NewHandler(svc, setup.WithAdminMiddleware(mw))
	mux := celltest.NewTestMux()
	require.NoError(t, h.RegisterRoutes(mux))
	return mux
}

// newHandlerWithBootstrapAuth creates a handler mux that wraps the admin
// endpoint with mock Basic Auth (simulating bootstrap middleware). Used by
// TestHandler_CreateAdmin_ValidCreds_BodyDifferentFromEnv_Returns201 which
// documents D5 semantics with a mock wrapper.
func newHandlerWithBootstrapAuth(t *testing.T, svc *setup.Handler, envUsername, envPassword string) http.Handler {
	t.Helper()
	mux := celltest.NewTestMux()
	require.NoError(t, svc.RegisterRoutes(mux))

	// Wrap the mux with a simple Basic Auth middleware that mirrors the
	// BootstrapMiddleware contract: correct creds → pass; wrong/missing → 401
	// with ERR_AUTH_BOOTSTRAP_FAILED.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != setupAdminPath || r.Method != http.MethodPost {
			mux.ServeHTTP(w, r)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != envUsername || pass != envPassword {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"ERR_AUTH_BOOTSTRAP_FAILED","message":"authentication required","details":{}}}`))
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// TestHandler_CreateAdmin_NoCreds_Returns401 verifies that when the handler is
// configured with bootstrap credentials, a request with no Authorization header
// is rejected with 401 ERR_AUTH_BOOTSTRAP_FAILED.
func TestHandler_CreateAdmin_NoCreds_Returns401(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	handler := newHandlerWithBootstrapCreds(t, svc, "op", "opSecret123")

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// No Authorization header.
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"admin endpoint without credentials must return 401")
	assert.Contains(t, w.Body.String(), "ERR_AUTH_BOOTSTRAP_FAILED")
}

// TestHandler_CreateAdmin_WrongUsername_Returns401 verifies that wrong username
// returns 401 with the same envelope as WrongPassword (oracle protection).
func TestHandler_CreateAdmin_WrongUsername_Returns401(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	handler := newHandlerWithBootstrapCreds(t, svc, "op", "opSecret123")

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("wronguser", "opSecret123")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"wrong username must return 401")
	assert.Contains(t, w.Body.String(), "ERR_AUTH_BOOTSTRAP_FAILED")
}

// TestHandler_CreateAdmin_ValidCreds_BodyDifferentFromEnv_Returns201 verifies
// D5 semantics: env credentials authenticate the operator; body credentials
// define the admin user. Env=op:opSecret123, body creates alice.
// RED: currently the endpoint is Public and does not require env credentials.
func TestHandler_CreateAdmin_ValidCreds_BodyDifferentFromEnv_Returns201(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})

	// In the target state, NewHandler accepts bootstrap credentials and enforces
	// Basic Auth. Here we use the mock wrapper to simulate that behavior.
	const envUser = "op"
	const envPass = "opSecret123"
	handler := newHandlerWithBootstrapAuth(t, setup.NewHandler(svc), envUser, envPass)

	// The body creates 'alice' — completely different from the env credentials.
	body := `{"username":"alice","email":"alice@example.com","password":"AlicePass!99"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Authenticate with env credentials (op:opSecret123), not alice's credentials.
	req.SetBasicAuth(envUser, envPass)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// With the mock wrapper, this should succeed (201) because env creds are correct
	// and the body is valid. This test documents D5: env=gate, body=identity.
	// RED aspect: the real bootstrap middleware is not wired, so this mock-based
	// test may PASS with the wrapper but the production path will be RED until Batch 2.
	if w.Code != http.StatusCreated {
		t.Errorf("TestHandler_CreateAdmin_ValidCreds_BodyDifferentFromEnv_Returns201: "+
			"expected 201 with valid env creds + valid body creating alice, got %d — "+
			"RED: bootstrap auth not wired in production handler yet (Batch 1+2)",
			w.Code)
		return
	}

	var resp struct {
		Data setup.CreateAdminOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "alice", resp.Data.Username,
		"D5: body username (alice) must be the created admin, not env username (op)")
}
