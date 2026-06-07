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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

// withTestTenantHeader adds the canonical test tenant ID as X-Tenant-ID header.
// The setup handler derives the tenant from this header (#1337 PR-2).
func withTestTenantHeader(req *http.Request) *http.Request {
	req.Header.Set("X-Tenant-ID", testTenantIDStr)
	return req
}

// testAllowAllLimiter satisfies auth.BootstrapRateLimiter for tests that
// focus on Basic Auth semantics. The authtest helper lives under
// runtime/internal/authtest (Go internal/ rule refuses cells/ imports at
// compile time, see issue #638), so this fake stays file-local; the
// equivalent fake also lives in cmd/corebundle/setup_integration_test.go
// (per-package duplication is the accepted cost of the visibility boundary).
type testAllowAllLimiter struct{}

func (testAllowAllLimiter) Allow(string) bool { return true }

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
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), &stubWriter{})
	return newHandlerMux(t, setup.NewHandler(svc, testPassthroughAuth))
}

// --- HandleStatus ---------------------------------------------------------

func TestHandler_Status_FreshSystem_ReturnsFalse(t *testing.T) {
	h := newHandlerFresh(t)
	w := httptest.NewRecorder()
	req := withTestTenantHeader(httptest.NewRequest(http.MethodGet, setupStatusPath, nil))

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data setup.StatusOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.False(t, body.Data.HasAdmin)
}

func TestHandler_Status_WithAdmin_ReturnsTrue(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, nil)
	h := newHandlerMux(t, setup.NewHandler(svc, testPassthroughAuth))

	w := httptest.NewRecorder()
	req := withTestTenantHeader(httptest.NewRequest(http.MethodGet, setupStatusPath, nil))
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
	req := withTestTenantHeader(httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body)))
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
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, &stubWriter{})
	h := newHandlerMux(t, setup.NewHandler(svc, testPassthroughAuth))

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := withTestTenantHeader(httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body)))
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
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedIdentityUser(t, userRepo, "root", "root@local")
	svc := newService(t, userRepo, roleRepo, &stubWriter{})
	h := newHandlerMux(t, setup.NewHandler(svc, testPassthroughAuth))

	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := withTestTenantHeader(httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body)))
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
	require.NoError(t, userRepo.Create(context.Background(), testTenantID, u))
}

// newHandlerWithBootstrapCreds creates a handler mux whose admin endpoint is
// protected by the real bootstrap middleware (runtime/auth.NewBootstrapMiddleware).
// Both NoCreds and WrongUsername/Password tests use this helper; the production
// wiring in cellmodules/accesscore/module.go follows the same WithBootstrapAuth +
// bootstrap middleware path.
//
// The rate limiter is testAllowAllLimiter (file-local; see top of file) —
// the focus is Basic Auth semantics, not throttling. The 429 path is covered
// by runtime/auth's own bootstrap_test.go via a configurable fake limiter.
func newHandlerWithBootstrapCreds(t *testing.T, svc *setup.Service, envUsername, envPassword string) http.Handler {
	t.Helper()
	creds := auth.BootstrapCredentials{
		Username: []byte(envUsername),
		Password: []byte(envPassword),
	}
	mw := auth.NewBootstrapMiddleware(creds, testAllowAllLimiter{}, nil)
	h := setup.NewHandler(svc, mw)
	mux := celltest.NewTestMux()
	require.NoError(t, h.RegisterRoutes(mux))
	return mux
}

// TestHandler_CreateAdmin_NoCreds_Returns401 verifies that when the handler is
// configured with bootstrap credentials, a request with no Authorization header
// is rejected with 401 ERR_AUTH_BOOTSTRAP_FAILED.
func TestHandler_CreateAdmin_NoCreds_Returns401(t *testing.T) {
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), &stubWriter{})
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
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), &stubWriter{})
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
//
// Uses the real runtime/auth.NewBootstrapMiddleware via newHandlerWithBootstrapCreds —
// the production path of cellmodules/accesscore/module.go. The "closed contract"
// codified in PR #392 review: NewHandler accepts bootstrapAuth as a required
// parameter; there is no separate "JWT-exempt + no auth" intermediate state.
func TestHandler_CreateAdmin_ValidCreds_BodyDifferentFromEnv_Returns201(t *testing.T) {
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), &stubWriter{})

	const envUser = "op"
	const envPass = "opSecret123"
	handler := newHandlerWithBootstrapCreds(t, svc, envUser, envPass)

	// The body creates 'alice' — completely different from the env credentials.
	body := `{"username":"alice","email":"alice@example.com","password":"AlicePass!99"}`
	req := withTestTenantHeader(httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	// Authenticate with env credentials (op:opSecret123), not alice's credentials.
	req.SetBasicAuth(envUser, envPass)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code,
		"valid env creds + valid body must create alice (D5 separation)")

	var resp struct {
		Data setup.CreateAdminOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "alice", resp.Data.Username,
		"D5: body username (alice) must be the created admin, not env username (op)")
}

// --- Missing X-Tenant-ID header tests (ADR 1160 fail behavior) -----------

// TestHandler_Status_NoTenantHeader_ReturnsFalse verifies the ADR 1160
// fail-soft design for the status endpoint: an absent X-Tenant-ID header
// causes StatusAdapter.Status to receive an empty XTenantID, which fails
// tenant.ParseTenantID — the adapter intentionally ignores the parse error
// and returns 200 HasAdmin:false (anti-enumeration: callers cannot infer
// whether a tenant exists or whether the header was simply missing).
func TestHandler_Status_NoTenantHeader_ReturnsFalse(t *testing.T) {
	h := newHandlerFresh(t)
	// Deliberately omit X-Tenant-ID header to exercise the fail-soft path.
	req := httptest.NewRequest(http.MethodGet, setupStatusPath, nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"status endpoint must return 200 (fail-soft) when X-Tenant-ID header is absent")
	var body struct {
		Data setup.StatusOutput `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.False(t, body.Data.HasAdmin,
		"HasAdmin must be false when tenant cannot be determined (ADR 1160 fail-soft)")
}

// TestHandler_CreateAdmin_NoTenantHeader_Returns400 verifies the ADR 1160
// fail-closed design for the admin-creation endpoint: an absent X-Tenant-ID
// header causes an empty XTenantID in the Request DTO, which
// validateCreateAdminInput rejects with 400 (RequireNotEmpty on tenantId)
// before any bcrypt or DB work is attempted.
func TestHandler_CreateAdmin_NoTenantHeader_Returns400(t *testing.T) {
	h := newHandlerFresh(t)
	// Deliberately omit X-Tenant-ID header to exercise the fail-closed path.
	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(http.MethodPost, setupAdminPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code,
		"admin endpoint must return 400 when X-Tenant-ID header is absent (ADR 1160 fail-closed)")
	assert.Contains(t, w.Body.String(), "ERR_AUTH_IDENTITY_INVALID_INPUT",
		"error code must indicate invalid input (empty tenantId via RequireNotEmpty)")
}
