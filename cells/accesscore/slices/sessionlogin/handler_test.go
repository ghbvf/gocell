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
)

// testIssuer is declared in service_test.go

func setup() *Handler {
	userRepo := mem.NewUserRepository()
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-pass"), bcrypt.MinCost)
	user := &domain.User{
		ID: "usr-1", Username: "alice", Email: "a@b.com",
		PasswordHash: string(hash), Status: domain.StatusActive,
	}
	_ = userRepo.Create(context.Background(), user)

	svc := NewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(), testIssuer, slog.Default())
	return NewHandler(svc)
}

func TestToTokenPairResponse_NilInput(t *testing.T) {
	var got dto.TokenPairResponse
	assert.NotPanics(t, func() { got = toTokenPairResponse(nil) })
	assert.Empty(t, got.AccessToken)
}

func TestTokenPairResponse_Fields(t *testing.T) {
	now := time.Now()
	pair := &TokenPair{
		AccessToken:  "access-tok-1",
		RefreshToken: "refresh-tok-1",
		ExpiresAt:    now,
	}
	resp := toTokenPairResponse(pair)

	assert.Equal(t, "access-tok-1", resp.AccessToken)
	assert.Equal(t, "refresh-tok-1", resp.RefreshToken)
	assert.Equal(t, now, resp.ExpiresAt)

	// Verify JSON key casing via serialization.
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"accessToken"`)
	assert.Contains(t, s, `"refreshToken"`)
	assert.Contains(t, s, `"expiresAt"`)
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
						AccessToken  string `json:"accessToken"`
						RefreshToken string `json:"refreshToken"`
						ExpiresAt    string `json:"expiresAt"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.NotEmpty(t, resp.Data.AccessToken)
				assert.NotEmpty(t, resp.Data.RefreshToken)
				assert.NotEmpty(t, resp.Data.ExpiresAt)

				// Verify camelCase JSON keys (#27n).
				var raw map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(body, &raw))
				var dataMap map[string]any
				require.NoError(t, json.Unmarshal(raw["data"], &dataMap))
				assert.Contains(t, dataMap, "accessToken", "key must be camelCase")
				assert.Contains(t, dataMap, "refreshToken", "key must be camelCase")
				assert.Contains(t, dataMap, "expiresAt", "key must be camelCase")
			},
		},
		{
			name:       "invalid JSON returns 400",
			body:       `{bad`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrong password returns 401",
			body:       `{"username":"alice","password":"wrong"}`,
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
			h := setup()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			h.HandleLogin(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// assertBlankFieldError is a helper that asserts the error response contains
// the expected errcode and a message stating "<fieldName> is required".
func assertBlankFieldError(t *testing.T, body []byte, wantCode, wantField string) {
	t.Helper()
	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	assert.Equal(t, wantCode, resp.Error.Code)
	assert.Equal(t, wantField+" is required", resp.Error.Message)
}

// TestHandler_Login_BlankUsername verifies that submitting an empty username
// returns 400 + ERR_AUTH_LOGIN_INVALID_INPUT + "username is required".
func TestHandler_Login_BlankUsername(t *testing.T) {
	h := setup()
	body := `{"username":"","password":"correct-pass"}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.HandleLogin(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assertBlankFieldError(t, w.Body.Bytes(), "ERR_AUTH_LOGIN_INVALID_INPUT", "username")
}

// TestHandler_Login_BlankPassword verifies that submitting an empty password
// returns 400 + ERR_AUTH_LOGIN_INVALID_INPUT + "password is required".
func TestHandler_Login_BlankPassword(t *testing.T) {
	h := setup()
	body := `{"username":"alice","password":""}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.HandleLogin(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assertBlankFieldError(t, w.Body.Bytes(), "ERR_AUTH_LOGIN_INVALID_INPUT", "password")
}
