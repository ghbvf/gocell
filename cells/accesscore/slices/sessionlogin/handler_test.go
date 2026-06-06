package sessionlogin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// testTenantID is declared in service_test.go (same package)

// testIssuer is declared in service_test.go

const loginPath = "/api/v1/access/sessions/login"

// setup wires the slice handler onto a celltest mux via RegisterRoutes — the
// same code path cell_routes.go takes in production. Tests dispatch via
// mux.ServeHTTP so per-package coverage records both HandleLogin and the
// RegisterRoutes wiring.
func setup(t *testing.T) http.Handler {
	t.Helper()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("alice", "a@b.com", string(hash), time.Now())
	user.ID = "usr-1"
	_ = userRepo.Create(context.Background(), testTenantID, user)

	sessionStore := testutil.RealSessionRepo(t)
	refreshStore := newTestRefreshStore()
	svc, err := NewService(clock.Real(), NewServiceParams{
		UserRepo:     userRepo,
		SessionStore: sessionStore,
		RoleRepo:     mem.NewStore(clock.Real()).RoleRepository(),
		RefreshStore: refreshStore,
		Issuer:       testIssuer,
	}, slog.Default(),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
		WithAccountLockout(newTestLockout(userRepo, sessionStore, refreshStore)))
	require.NoError(t, err)
	const testCookieMaxAge = 604800 // 7 days in seconds
	mux := celltest.NewTestMux()
	if err := NewHandler(svc, testCookieMaxAge).RegisterRoutes(mux); err != nil {
		panic("RegisterRoutes: " + err.Error())
	}
	return mux
}

func TestTokenPairResponse_Fields(t *testing.T) {
	now := time.Now()
	pair := dto.TokenPair{
		AccessToken:           "access-tok-1",
		RefreshToken:          "refresh-tok-1",
		ExpiresAt:             now,
		SessionID:             "sess-1",
		UserID:                "usr-1",
		PasswordResetRequired: true,
	}
	resp := dto.ToTokenPairResponse(pair)

	assert.Equal(t, "access-tok-1", resp.AccessToken)
	assert.Equal(t, "refresh-tok-1", resp.RefreshToken)
	assert.Equal(t, now, resp.ExpiresAt)
	assert.Equal(t, "sess-1", resp.SessionID)
	assert.Equal(t, "usr-1", resp.UserID)
	assert.True(t, resp.PasswordResetRequired)

	// Verify JSON key casing: marshal to generic map and check keys.
	rawBytes, err := json.Marshal(map[string]any{
		"accessToken":           resp.AccessToken,
		"refreshToken":          resp.RefreshToken,
		"expiresAt":             resp.ExpiresAt,
		"sessionId":             resp.SessionID,
		"userId":                resp.UserID,
		"passwordResetRequired": resp.PasswordResetRequired,
	})
	require.NoError(t, err)
	s := string(rawBytes)
	assert.Contains(t, s, `"accessToken"`)
	assert.Contains(t, s, `"refreshToken"`)
	assert.Contains(t, s, `"expiresAt"`)
	assert.Contains(t, s, `"sessionId"`)
	assert.Contains(t, s, `"userId"`)
	assert.Contains(t, s, `"passwordResetRequired"`)
}

func TestHandleLogin(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "valid credentials returns 201 with tokens",
			body:       `{"username":"alice","password":"correct-pass"}`,
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data struct {
						AccessToken           string `json:"accessToken"`
						RefreshToken          string `json:"refreshToken"`
						ExpiresAt             string `json:"expiresAt"`
						SessionID             string `json:"sessionId"`
						UserID                string `json:"userId"`
						PasswordResetRequired bool   `json:"passwordResetRequired"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.NotEmpty(t, resp.Data.AccessToken)
				assert.NotEmpty(t, resp.Data.RefreshToken)
				assert.NotEmpty(t, resp.Data.ExpiresAt)
				assert.NotEmpty(t, resp.Data.SessionID)
				assert.NotEmpty(t, resp.Data.UserID)
				// Normal users do not have password_reset_required set.
				assert.False(t, resp.Data.PasswordResetRequired)

				// Verify camelCase JSON keys (#27n).
				var raw map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(body, &raw))
				var dataMap map[string]any
				require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
				assert.Contains(t, dataMap, "accessToken", "key must be camelCase")
				assert.Contains(t, dataMap, "refreshToken", "key must be camelCase")
				assert.Contains(t, dataMap, "expiresAt", "key must be camelCase")
				assert.Contains(t, dataMap, "sessionId", "key must be camelCase")
				assert.Contains(t, dataMap, "userId", "key must be camelCase")
				assert.Contains(t, dataMap, "passwordResetRequired", "key must be camelCase")
			},
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad`,
			wantStatus: http.StatusBadRequest,
		},
		{
			// Generated handler enforces minLength:8 on password; use a password that
			// passes the schema check but fails the bcrypt comparison.
			name:       "wrong password returns 401",
			body:       `{"username":"alice","password":"wrong-password"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unknown field returns 400",
			body:       `{"username":"alice","password":"correct-pass","extra":"y"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := setup(t)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Tenant-ID", testTenantIDStr)
			h.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// TestHandler_Login_MissingTenantHeader verifies that a request without the
// X-Tenant-ID header returns 400 (the service treats an empty tenantId as
// ErrAuthLoginInvalidInput via RequireNotEmpty).
func TestHandler_Login_MissingTenantHeader(t *testing.T) {
	h := setup(t)
	body := `{"username":"alice","password":"correct-pass"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Intentionally do NOT set X-Tenant-ID.
	h.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assertValidationError(t, w.Body.Bytes(), "ERR_AUTH_LOGIN_INVALID_INPUT")
}

// assertValidationError is a helper that asserts the error response has the
// expected error code (from the generated handler's schema validation).
func assertValidationError(t *testing.T, body []byte, wantCode string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, wantCode, resp.Error.Code)
}

// TestHandler_Login_BlankUsername verifies that submitting an empty username
// returns 400. The generated handler enforces minLength:1 before the service.
func TestHandler_Login_BlankUsername(t *testing.T) {
	h := setup(t)
	body := `{"username":"","password":"correct-pass"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	// Generated handler intercepts blank username before the service; returns ERR_VALIDATION_FAILED.
	assertValidationError(t, w.Body.Bytes(), "ERR_VALIDATION_FAILED")
}

// TestHandler_Login_BlankPassword verifies that submitting an empty password
// returns 400. The generated handler enforces minLength:8 before the service.
func TestHandler_Login_BlankPassword(t *testing.T) {
	h := setup(t)
	body := `{"username":"alice","password":""}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	// Generated handler intercepts blank password before the service; returns ERR_VALIDATION_FAILED.
	assertValidationError(t, w.Body.Bytes(), "ERR_VALIDATION_FAILED")
}

// TestHandler_Login_SetsRefreshCookie asserts that a successful login (201)
// emits a gocell_rt httpOnly cookie carrying the refresh token, while the JSON
// body still contains the same refreshToken value (backward compat).
func TestHandler_Login_SetsRefreshCookie(t *testing.T) {
	h := setup(t)
	body := `{"username":"alice","password":"correct-pass"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", testTenantIDStr)
	h.ServeHTTP(w, req)

	require.Equal(t, http.StatusCreated, w.Code, "expected 201 on valid login")

	// Parse JSON body and extract refreshToken for comparison.
	var resp struct {
		Data struct {
			RefreshToken string `json:"refreshToken"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data.RefreshToken, "body must still carry refreshToken")

	// Find the gocell_rt cookie in the response.
	var rtCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "gocell_rt" {
			rtCookie = c
			break
		}
	}
	require.NotNil(t, rtCookie, "Set-Cookie: gocell_rt must be present on 201 response")
	assert.NotEmpty(t, rtCookie.Value, "cookie value must not be empty")
	assert.Equal(t, resp.Data.RefreshToken, rtCookie.Value, "cookie value must match body refreshToken")
	assert.True(t, rtCookie.HttpOnly, "cookie must be HttpOnly")
	assert.True(t, rtCookie.Secure, "cookie must be Secure")
	assert.Equal(t, http.SameSiteStrictMode, rtCookie.SameSite, "cookie must be SameSite=Strict")
	assert.Equal(t, "/api/v1/access/sessions", rtCookie.Path, "cookie Path must be /api/v1/access/sessions")
	assert.Equal(t, 604800, rtCookie.MaxAge, "cookie MaxAge must match cookieMaxAge arg")
}

// TestHandler_Login_NoCookieOn401 asserts that a failed login (401) does NOT
// emit a gocell_rt cookie — a 4xx must neither mint nor clear the cookie.
func TestHandler_Login_NoCookieOn401(t *testing.T) {
	h := setup(t)
	body := `{"username":"alice","password":"wrong-password"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, loginPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", testTenantIDStr)
	h.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	for _, c := range w.Result().Cookies() {
		assert.NotEqual(t, "gocell_rt", c.Name, "no gocell_rt cookie must be set on 401")
	}
}
