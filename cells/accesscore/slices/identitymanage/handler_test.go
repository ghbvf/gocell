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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

const invalidUUID = "not-a-uuid-string"

func newHandlerIdentityRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{ReuseInterval: testtime.D2s, MaxAge: time.Hour}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

// handlerStubIssuer is a minimal TokenIssuer stub used by handler tests that
// do not exercise the ChangePassword token-issuing path.
var handlerStubIssuer TokenIssuer = &stubTokenIssuer{}

func setup(t testing.TB) http.Handler {
	t.Helper()
	svc, err := NewService(mem.NewUserRepository(), testutil.RealSessionRepo(t), newHandlerIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(handlerStubIssuer), WithClock(clock.Real()), WithTxManager(contractTxRunner{}))
	if err != nil {
		panic("setup: " + err.Error())
	}
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/api/v1/access/users", func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			panic("setup: RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

// setupWithIssuer wires a service with a stub TokenIssuer for ChangePassword tests.
func setupWithIssuer(t testing.TB, issuer TokenIssuer) (http.Handler, *mem.UserRepository) {
	t.Helper()
	repo := mem.NewUserRepository()
	effectiveIssuer := issuer
	if effectiveIssuer == nil {
		effectiveIssuer = handlerStubIssuer
	}
	svc, err := NewService(repo, testutil.RealSessionRepo(t), newHandlerIdentityRefreshStore(), slog.Default(),
		WithTokenIssuer(effectiveIssuer), WithClock(clock.Real()), WithTxManager(contractTxRunner{}))
	if err != nil {
		panic("setupWithIssuer: " + err.Error())
	}
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/api/v1/access/users", func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			panic("setupWithIssuer: RegisterRoutes: " + err.Error())
		}
	})
	return mux, repo
}

// prefixPath helpers prepend the canonical API prefix so legacy relative
// request paths continue to read clearly in test tables.
const identityPrefix = "/api/v1/access/users"

// adminCtx returns a context carrying admin credentials for test requests.
func adminCtx() func(*http.Request) *http.Request {
	return func(req *http.Request) *http.Request {
		return req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	}
}

func TestToUserResponseData_NilInput(t *testing.T) {
	// toUserResponseData must not panic on nil input and must return empty strings.
	var id, username, email, status, createdAt, updatedAt string
	assert.NotPanics(t, func() {
		id, username, email, status, createdAt, updatedAt = toUserResponseData(nil)
	})
	assert.Empty(t, id)
	assert.Empty(t, username)
	assert.Empty(t, email)
	assert.Empty(t, status)
	assert.Empty(t, createdAt)
	assert.Empty(t, updatedAt)
}

func TestToUserResponseData_Fields(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	user := &domain.User{
		ID: "u1", Username: "alice", Email: "a@b.com",
		PasswordHash: "secret-hash-bcrypt", Status: domain.StatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	id, username, email, status, createdAt, updatedAt := toUserResponseData(user)

	assert.Equal(t, "u1", id)
	assert.Equal(t, "alice", username)
	assert.Equal(t, "a@b.com", email)
	assert.Equal(t, "active", status)

	// Timestamps are formatted as RFC3339; verify they do not contain the hash.
	assert.NotContains(t, createdAt, "secret-hash-bcrypt")
	assert.NotContains(t, updatedAt, "secret-hash-bcrypt")
	// RFC3339 format check.
	assert.Contains(t, createdAt, "T")
	assert.Contains(t, updatedAt, "T")
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
			path:       "/" + testutil.TestID("no-such-id"),
			subject:    testutil.TestID("no-such-id"), // self-access
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
			path:       "/" + testutil.TestID("self-access-test"),
			subject:    testutil.TestID("self-access-test"),
			wantStatus: http.StatusNotFound, // authz passes (self), service returns 404
		},
		{
			name:       "GET /{id} different user non-admin returns 403",
			method:     http.MethodGet,
			path:       "/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-2"),
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{id} no auth returns 401",
			method:     http.MethodGet,
			path:       "/" + testutil.TestID("user-1"),
			subject:    "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "DELETE /{id} non-admin returns 403",
			method:     http.MethodDelete,
			path:       "/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-1"), // even self cannot delete (admin check fires first)
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "DELETE /{id} admin self-delete returns 409",
			method:     http.MethodDelete,
			path:       "/" + testutil.TestID("admin-1"),
			subject:    testutil.TestID("admin-1"),
			roles:      []string{"admin"},
			wantStatus: http.StatusConflict,
		},
		{
			// Documents the check order: admin role check (403) fires before
			// self-delete check (409). Non-admins cannot reach the self-delete guard.
			name:       "DELETE /{id} non-admin self-delete still returns 403",
			method:     http.MethodDelete,
			path:       "/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-1"),
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST /{id}/lock non-admin returns 403",
			method:     http.MethodPost,
			path:       "/" + testutil.TestID("user-1") + "/lock",
			subject:    testutil.TestID("user-1"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "POST /{id}/unlock non-admin returns 403",
			method:     http.MethodPost,
			path:       "/" + testutil.TestID("user-1") + "/unlock",
			subject:    testutil.TestID("user-1"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{id} invalid UUID returns 400",
			method:     http.MethodGet,
			path:       "/" + invalidUUID,
			subject:    "admin-user",
			roles:      []string{"admin"},
			wantStatus: http.StatusBadRequest,
			checkBody: func(t *testing.T, body []byte) {
				var b struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(body, &b))
				assert.Equal(t, string(errcode.ErrValidationInvalidUUID), b.Error.Code)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup(t)
			reqPath := identityPrefix
			if tc.path != "/" {
				reqPath += tc.path
			}
			var req *http.Request
			if tc.body != "" {
				req = httptest.NewRequest(tc.method, reqPath, strings.NewReader(tc.body))
			} else {
				req = httptest.NewRequest(tc.method, reqPath, nil)
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
	r := setup(t)

	// Create a user first (as admin).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"bob","email":"b@c.com","password":"pass1234"}`))
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
	req = httptest.NewRequest(http.MethodPut, identityPrefix+"/"+created.Data.ID,
		strings.NewReader(`{"email":"new@b.com","extra":"y"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(created.Data.ID, nil)) // self-access
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_PatchRejectsUnknownFields(t *testing.T) {
	r := setup(t)

	// Create a user first (as admin).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"eve","email":"e@f.com","password":"pass1234"}`))
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

	// PATCH request schema is strict; unknown fields should fail fast instead
	// of being silently ignored.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+created.Data.ID,
		strings.NewReader(`{"email":"new@f.com","extra":"ignored"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(created.Data.ID, nil)) // self-access
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code, "PATCH must reject unknown fields to match additionalProperties:false")
}

func TestHandler_CreateThenGetThenDelete(t *testing.T) {
	r := setup(t)

	// Create (admin).
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"bob","email":"b@c.com","password":"pass1234"}`))
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
	getReq := httptest.NewRequest(http.MethodGet, identityPrefix+"/"+id, nil)
	getReq = getReq.WithContext(auth.TestContext(id, nil))
	r.ServeHTTP(w, getReq)
	assert.Equal(t, http.StatusOK, w.Code)

	// Delete (admin).
	w = httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, identityPrefix+"/"+id, nil)
	delReq = adminCtx()(delReq)
	r.ServeHTTP(w, delReq)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestHandlePatch_TypeValidation(t *testing.T) {
	r := setup(t)

	// Create a user first (admin).
	w := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, identityPrefix,
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
			req := httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+id, strings.NewReader(tc.body))
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

// seedUserInRepo creates a user with a bcrypt-hashed "oldpass12" password directly in the repo.
// Uses "oldpass12" (9 chars) to satisfy the generated handler's minLength:8 constraint.
func seedUserInRepo(t *testing.T, repo *mem.UserRepository, id, username string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("oldpass12"), bcrypt.MinCost)
	require.NoError(t, err)
	user, err := domain.NewUser(username, username+"@test.com", string(hash), time.Now())
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
	r, repo := setupWithIssuer(t, stubIssuer)
	seedUserInRepo(t, repo, testutil.TestID("usr-self"), "self-user")

	// Passwords must be ≥ 8 chars to pass the generated handler's minLength check.
	body := `{"oldPassword":"oldpass12","newPassword":"newpass12"}`
	req := httptest.NewRequest(http.MethodPost, identityPrefix+"/"+testutil.TestID("usr-self")+"/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testutil.TestID("usr-self"), nil)) // self-access
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "new-access-token")
	assert.Contains(t, w.Body.String(), `"passwordResetRequired":false`)
}

func TestHandler_ChangePassword_AdminOnAnotherUser_Allowed(t *testing.T) {
	issuedAT := "admin-issued-" + "at"
	issuedRT := "admin-issued-" + "rt"
	stubIssuer := &stubTokenIssuer{pair: dto.TokenPair{
		AccessToken:  issuedAT,
		RefreshToken: issuedRT,
	}}
	r, repo := setupWithIssuer(t, stubIssuer)
	seedUserInRepo(t, repo, testutil.TestID("usr-target"), "target-user")

	// Passwords must be ≥ 8 chars to pass the generated handler's minLength check.
	body := `{"oldPassword":"oldpass12","newPassword":"newpass12"}`
	req := httptest.NewRequest(http.MethodPost, identityPrefix+"/"+testutil.TestID("usr-target")+"/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "admin-issued-at")
}

func TestHandler_ChangePassword_StrangerForbidden(t *testing.T) {
	r, repo := setupWithIssuer(t, nil)
	seedUserInRepo(t, repo, testutil.TestID("usr-victim"), "victim-user")

	body := `{"oldPassword":"oldpass12","newPassword":"newpass12"}`
	req := httptest.NewRequest(http.MethodPost, identityPrefix+"/"+testutil.TestID("usr-victim")+"/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testutil.TestID("usr-stranger"), []string{"viewer"})) // not self, not admin
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestHandler_ChangePassword_BadJSON(t *testing.T) {
	r, repo := setupWithIssuer(t, nil)
	seedUserInRepo(t, repo, testutil.TestID("usr-badjson"), "badjson-user")

	req := httptest.NewRequest(http.MethodPost, identityPrefix+"/"+testutil.TestID("usr-badjson")+"/password", strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext(testutil.TestID("usr-badjson"), nil)) // self
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestHandler_Create_RequirePasswordResetField(t *testing.T) {
	r := setup(t)

	// Create with requirePasswordReset=true.
	// Password must be ≥ 8 chars to pass the generated handler's minLength check.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"flagged","email":"f@g.com","password":"pass1234","requirePasswordReset":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)

	// Verify no password hash leaks and response has expected shape.
	assert.NotContains(t, w.Body.String(), "passwordHash")
}

// ---------------------------------------------------------------------------
// PATCH semantics: *bool distinguishes absent / true / false
// These three tests are the RED anchor for PATCH-OPTIONAL-BOOL-POINTER-01.
// Before the fix, RequirePasswordReset is bare bool, so PATCH false == absent.
// After the fix, types_gen.go generates *bool, handler passes it directly, and
// service.UpdateInput.RequirePasswordReset *bool handles nil/false/true correctly.
// ---------------------------------------------------------------------------

// TestPatch_RequirePasswordResetFalse_Clears verifies that PATCH
// {"requirePasswordReset": false} explicitly clears the flag.
// RED: currently fails because bare bool cannot distinguish false from absent.
func TestPatch_RequirePasswordResetFalse_Clears(t *testing.T) {
	r, repo := setupWithIssuer(t, handlerStubIssuer)

	// Create user with requirePasswordReset=true.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"clruser","email":"clr@test.com","password":"pass1234","requirePasswordReset":true}`))
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

	// Confirm the flag is set.
	user, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.True(t, user.PasswordResetRequired, "flag should be set after create")

	// PATCH with {"requirePasswordReset": false} — must clear the flag.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+id,
		strings.NewReader(`{"requirePasswordReset":false}`))
	req.Header.Set("Content-Type", "application/json")
	req = adminCtx()(req)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "PATCH false must return 200")

	// After PATCH, flag must be cleared.
	user, err = repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.False(t, user.PasswordResetRequired, "PATCH false must clear the flag (not treat it as no-change)")
}

// TestPatch_RequirePasswordResetTrue_Sets verifies that PATCH
// {"requirePasswordReset": true} sets the flag.
func TestPatch_RequirePasswordResetTrue_Sets(t *testing.T) {
	r, repo := setupWithIssuer(t, handlerStubIssuer)

	// Create user without requirePasswordReset.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"setuser","email":"set@test.com","password":"pass1234"}`))
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

	user, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.False(t, user.PasswordResetRequired, "flag should not be set after create without it")

	// PATCH with {"requirePasswordReset": true} — must set the flag.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+id,
		strings.NewReader(`{"requirePasswordReset":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = adminCtx()(req)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	user, err = repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.True(t, user.PasswordResetRequired, "PATCH true must set the flag")
}

// TestPatch_RequirePasswordResetAbsent_NoChange verifies that PATCH {}
// (absent field) does NOT change the flag.
func TestPatch_RequirePasswordResetAbsent_NoChange(t *testing.T) {
	r, repo := setupWithIssuer(t, handlerStubIssuer)

	// Create user with requirePasswordReset=true.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"ncuser","email":"nc@test.com","password":"pass1234","requirePasswordReset":true}`))
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

	// PATCH with {} — field absent, must not change the flag.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+id,
		strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req = adminCtx()(req)
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	user, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.True(t, user.PasswordResetRequired, "PATCH with absent field must not change the flag")
}

func TestHandler_Patch_RequirePasswordResetField(t *testing.T) {
	r := setup(t)

	// Create a user first.
	// Password must be ≥ 8 chars to pass the generated handler's minLength check.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, identityPrefix,
		strings.NewReader(`{"username":"patchy","email":"p@y.com","password":"pass1234"}`))
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
	req = httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+created.Data.ID,
		strings.NewReader(`{"requirePasswordReset":true}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// PATCH with invalid type for requirePasswordReset.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, identityPrefix+"/"+created.Data.ID,
		strings.NewReader(`{"requirePasswordReset":"yes"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("admin-user", []string{"admin"}))
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_VALIDATION_FAILED")
}
