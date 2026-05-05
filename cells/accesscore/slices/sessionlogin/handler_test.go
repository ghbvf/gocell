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
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testIssuer is declared in service_test.go

const loginPath = "/api/v1/access/sessions/login"

func newHandlerRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

// setup wires the slice handler onto a celltest mux via RegisterRoutes — the
// same code path cell_routes.go takes in production. Tests dispatch via
// mux.ServeHTTP so per-package coverage records both HandleLogin and the
// RegisterRoutes wiring.
func setup(t *testing.T) http.Handler {
	t.Helper()
	userRepo := mem.NewUserRepository()
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-pass"), bcrypt.MinCost)
	user := &domain.User{
		ID: "usr-1", Username: "alice", Email: "a@b.com",
		PasswordHash: string(hash), Status: domain.StatusActive,
	}
	_ = userRepo.Create(context.Background(), user)

	svc := MustNewService(userRepo, testutil.RealSessionRepo(t), mem.NewRoleRepository(),
		newHandlerRefreshStore(), testIssuer, slog.Default(), WithClock(clock.Real()), WithTxManager(&stubTxRunner{}))
	mux := celltest.NewTestMux()
	if err := NewHandler(svc).RegisterRoutes(mux); err != nil {
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
			h.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
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
