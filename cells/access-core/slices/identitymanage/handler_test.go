package identitymanage

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// handlerStubIssuer is a minimal TokenIssuer stub used by handler tests that
// do not exercise the ChangePassword token-issuing path.
var handlerStubIssuer TokenIssuer = &stubTokenIssuer{}

func setup() http.Handler {
	svc, err := NewService(mem.NewUserRepository(), mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithTokenIssuer(handlerStubIssuer))
	if err != nil {
		panic("setup: " + err.Error())
	}
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux
}

// setupWithIssuer wires a service with a stub TokenIssuer for ChangePassword tests.
func setupWithIssuer(issuer TokenIssuer) (http.Handler, *mem.UserRepository) {
	repo := mem.NewUserRepository()
	effectiveIssuer := issuer
	if effectiveIssuer == nil {
		effectiveIssuer = handlerStubIssuer
	}
	svc, err := NewService(repo, mem.NewSessionRepository(), eventbus.New(), slog.Default(),
		WithTokenIssuer(effectiveIssuer))
	if err != nil {
		panic("setupWithIssuer: " + err.Error())
	}
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux, repo
}

// adminCtx returns a context carrying admin credentials for test requests.
func adminCtx() func(*http.Request) *http.Request {
	return func(req *http.Request) *http.Request {
		return req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	}
}

func TestToUserResponse_NilInput(t *testing.T) {
	var got UserResponse
	assert.NotPanics(t, func() { got = toUserResponse(nil) })
	assert.Zero(t, got.ID)
}

func TestUserResponse_ExcludesSensitiveFields(t *testing.T) {
	now := time.Now()
	user := &domain.User{
		ID: "u1", Username: "alice", Email: "a@b.com",
		PasswordHash: "secret-hash-bcrypt", Status: domain.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	resp := toUserResponse(user)

	assert.Equal(t, "u1", resp.ID)
	assert.Equal(t, "alice", resp.Username)
	assert.Equal(t, "a@b.com", resp.Email)
	assert.Equal(t, "active", resp.Status)
	assert.Equal(t, now, resp.CreatedAt)
	assert.Equal(t, now, resp.UpdatedAt)

	// Verify sensitive fields are not serialized.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.NotContains(t, s, "secret-hash-bcrypt")
	assert.NotContains(t, s, "passwordHash")

	// Verify camelCase JSON keys (#27n).
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"username"`)
	assert.Contains(t, s, `"email"`)
	assert.Contains(t, s, `"status"`)
	assert.Contains(t, s, `"createdAt"`)
	assert.Contains(t, s, `"updatedAt"`)
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		subject    string
		roles      []string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "POST / valid user returns 201",
			method:     http.MethodPost,
			path:       "/",
			body:       `{"username":"alice","email":"a@b.com","password":"secret123"}`,
			subject:    "admin-user",
			roles:      []string{"admin"},
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var resp map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Contains(t, string(resp["data"]), "alice")

				// Verify camelCase JSON keys (#27n).
				var dataMap map[string]any
				require.NoError(t, json.Unmarshal(resp["data"], &dataMap))
				assert.Contains(t, dataMap, "id", "key must be camelCase")
				assert.Contains(t, dataMap, "username", "key must be camelCase")
				assert.Contains(t, dataMap, "email", "key must be camelCase")
				assert.Contains(t, dataMap, "status", "key must be camelCase")
				assert.Contains(t, dataMap, "createdAt", "key must be camelCase")
				assert.Contains(t, dataMap, "updatedAt", "key must be camelCase")
			},
		},
		{
			name:       "POST / invalid body returns 400",
			method:     http.MethodPost,
			path:       "/",
			body:       `{bad json`,
			subject:    "admin-user",
			roles:      []string{"admin"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "GET /{id} nonexistent returns 404",
			method:     http.MethodGet,
			path:       "/no-such-id",
			subject:    "no-such-id", // self-access
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "POST / unknown field returns 400",
			method:     http.MethodPost,
			path:       "/",
			body:       `{"username":"alice","email":"a@b.com","password":"secret123","extra":"y"}`,
			subject:    "admin-user",
			roles:      []string{"admin"},
			wantStatus: http.StatusBadRequest,
		},
		// Authorization tests (H1-2).
		{
			name:       "POST / no auth returns 401",
			method:     http.MethodPost,
			path:       "/",
			body:       `{"username":"alice","email":"a@b.com","password":"secret123"}`,
			subject:    "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "POST / non-admin returns 403",
			method:     http.MethodPost,
			path:       "/",
			body:       `{"username":"alice","email":"a@b.com","password":"secret123"}`,
			subject:    "user-1",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{id} self-access authz passes (user not found)",
			method:     http.MethodGet,
			path:       "/self-access-test",
			subject:    "self-access-test",
			wantStatus: http.StatusNotFound, // authz passes (self), service returns 404
		},
		{
			name:       "GET /{id} different user non-admin returns 403",
			method:     http.MethodGet,
			path:       "/user-1",
			subject:    "user-2",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{id} no auth returns 401",
			method:     http.MethodGet,
			path:       "/user-1",
			subject:    "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "DELETE /{id} non-admin returns 403",
			method:     http.MethodDelete,
			path:       "/user-1",
			subject:    "user-1", // even self cannot delete
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "DELETE /{id} admin self-delete returns 409",
			method:     http.MethodDelete,
			path:       "/admin-1",
			subject:    "admin-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusConflict,
		},
		{
			// Documents the check order: admin role check (403) fires before
			// self-delete check (409). Non-admins cannot reach the self-delete guard.
			name:       "DELETE /{id} non-admin self-delete still returns 403",
			method:     http.MethodDelete,
			path:       "/user-1",
			subject:    "user-1",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST /{id}/lock non-admin returns 403",
			method:     http.MethodPost,
			path:       "/user-1/lock",
			subject:    "user-1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST /{id}/unlock non-admin returns 403",
			method:     http.MethodPost,
			path:       "/user-1/unlock",
			subject:    "user-1",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup()
			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			} else {
				req = httptest.NewRequest(tc.method, tc.path, nil)
			}
			req.Header.Set("Content-Type", "application/json")
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandler_UpdateUnknownField(t *testing.T) {
	r := setup()

	// Create a user first (as admin).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"bob","email":"b@c.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	req = adminCtx()(req)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	// PUT with unknown field should return 400 (self-access).
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/"+created.Data.ID,
		strings.NewReader(`{"email":"new@b.com","extra":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(created.Data.ID, nil)) // self-access
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_PatchAcceptsUnknownFields(t *testing.T) {
	r := setup()

	// Create a user first (as admin).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"eve","email":"e@f.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	req = adminCtx()(req)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	// PATCH with unknown field should succeed (merge patch accepts any key, self-access).
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/"+created.Data.ID,
		strings.NewReader(`{"email":"new@f.com","extra":"ignored"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(created.Data.ID, nil)) // self-access
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code, "PATCH uses DecodeJSON (not strict); unknown fields must be accepted for merge patch semantics")
}

func TestHandler_CreateThenGetThenDelete(t *testing.T) {
	r := setup()

	// Create (admin).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"username":"bob","email":"b@c.com","password":"pass1234"}`))
	req.Header.Set("Content-Type", "application/json")
	req = adminCtx()(req)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.Data.ID
	require.NotEmpty(t, id)

	// Get (self-access).
	w = httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/"+id, nil)
	getReq = getReq.WithContext(auth.TestContext(id, nil))
	r.ServeHTTP(w, getReq)
	assert.Equal(t, http.StatusOK, w.Code)

	// Delete (admin).
	w = httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/"+id, nil)
	delReq = adminCtx()(delReq)
	r.ServeHTTP(w, delReq)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandlePatch_TypeValidation(t *testing.T) {
	r := setup()

	// Create a user first (admin).
	w := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"patchuser","email":"p@b.com","password":"Secret123!"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq = adminCtx()(createReq)
	r.ServeHTTP(w, createReq)
	require.Equal(t, http.StatusCreated, w.Code)
	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))
	id := created.Data.ID

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "valid string fields accepted",
			body:       `{"name":"new-name"}`,
			wantStatus: http.StatusOK,
		},
		{
			name:       "name as number returns 400",
			body:       `{"name":123}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_FAILED",
		},
		{
			name:       "email as boolean returns 400",
			body:       `{"email":true}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_FAILED",
		},
		{
			name:       "status as array returns 400",
			body:       `{"status":["active"]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "ERR_VALIDATION_FAILED",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPatch, "/"+id, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(auth.TestContext(id, nil)) // self-access
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.wantCode != "" {
				assert.Contains(t, w.Body.String(), tc.wantCode)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// handleChangePassword tests
// ---------------------------------------------------------------------------

// seedUserInRepo creates a user with a bcrypt-hashed password directly in the repo.
func seedUserInRepo(t *testing.T, repo *mem.UserRepository, id, username, password string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := domain.NewUser(username, username+"@test.com", string(hash))
	require.NoError(t, err)
	user.ID = id
	require.NoError(t, repo.Create(context.Background(), user))
}

func TestHandler_ChangePassword_SelfAllowed(t *testing.T) {
	stubIssuer := &stubTokenIssuer{pair: dto.TokenPair{
		AccessToken:           "new-access-token",
		RefreshToken:          "new-refresh-token",
		PasswordResetRequired: false,
	}}
	r, repo := setupWithIssuer(stubIssuer)
	seedUserInRepo(t, repo, "usr-self", "self-user", "oldpass")

	body := `{"oldPassword":"oldpass","newPassword":"newpass"}`
	req := httptest.NewRequest(http.MethodPost, "/usr-self/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("usr-self", nil)) // self-access
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new-access-token")
	assert.Contains(t, w.Body.String(), `"passwordResetRequired":false`)
}

func TestHandler_ChangePassword_AdminOnAnotherUser_Allowed(t *testing.T) {
	stubIssuer := &stubTokenIssuer{pair: dto.TokenPair{
		AccessToken:  "admin-issued-at",
		RefreshToken: "admin-issued-rt",
	}}
	r, repo := setupWithIssuer(stubIssuer)
	seedUserInRepo(t, repo, "usr-target", "target-user", "oldpass")

	body := `{"oldPassword":"oldpass","newPassword":"newpass2"}`
	req := httptest.NewRequest(http.MethodPost, "/usr-target/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "admin-issued-at")
}

func TestHandler_ChangePassword_StrangerForbidden(t *testing.T) {
	r, repo := setupWithIssuer(nil)
	seedUserInRepo(t, repo, "usr-victim", "victim-user", "oldpass")

	body := `{"oldPassword":"oldpass","newPassword":"newpass"}`
	req := httptest.NewRequest(http.MethodPost, "/usr-victim/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("usr-stranger", []string{"viewer"})) // not self, not admin
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandler_ChangePassword_BadJSON(t *testing.T) {
	r, repo := setupWithIssuer(nil)
	seedUserInRepo(t, repo, "usr-badjson", "badjson-user", "oldpass")

	req := httptest.NewRequest(http.MethodPost, "/usr-badjson/password", strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("usr-badjson", nil)) // self
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_Create_RequirePasswordResetField(t *testing.T) {
	r := setup()

	// Create with requirePasswordReset=true.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"flagged","email":"f@g.com","password":"pass","requirePasswordReset":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	// Verify no password hash leaks and response has expected shape.
	assert.NotContains(t, w.Body.String(), "passwordHash")
}

func TestHandler_Patch_RequirePasswordResetField(t *testing.T) {
	r := setup()

	// Create a user first.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/",
		strings.NewReader(`{"username":"patchy","email":"p@y.com","password":"pass"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	var created struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &created))

	// PATCH with requirePasswordReset=true (admin sets flag).
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/"+created.Data.ID,
		strings.NewReader(`{"requirePasswordReset":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// PATCH with invalid type for requirePasswordReset.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/"+created.Data.ID,
		strings.NewReader(`{"requirePasswordReset":"yes"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}
