package sessionrefresh

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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
)

// testIssuer is declared in service_test.go

func setup() (*Handler, string) {
	sessionRepo := mem.NewSessionRepository()
	refreshStore := newTestRefreshStore()

	sess, _ := domain.NewSession("usr-1", "access-tok", time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)

	// Issue an opaque wire token for sess-1.
	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-1", "usr-1")
	if err != nil {
		panic("setup: issue refresh token: " + err.Error())
	}

	// F1 fail-closed requires the session's user to be resolvable; seed a user
	// so rotateAndIssue does not abort.
	userRepo := mem.NewUserRepository()
	u, _ := domain.NewUser("usr-1", "usr-1@test.local", "hash")
	u.ID = "usr-1"
	_ = userRepo.Create(context.Background(), u)

	svc := NewService(sessionRepo, mem.NewRoleRepository(), userRepo, refreshStore, testIssuer, slog.Default())
	return NewHandler(svc), wireToken
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

func TestHandleRefresh(t *testing.T) {
	h, validToken := setup()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "valid refresh token returns 200 with new tokens",
			body:       `{"refreshToken":"` + validToken + `"}`,
			wantStatus: http.StatusOK,
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
			name:       "invalid token returns 401",
			body:       `{"refreshToken":"not.a.valid.opaque.token"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unknown field returns 400",
			body:       `{"refreshToken":"not.a.valid.opaque.token","extra":"y"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			h.HandleRefresh(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// TestHandler_Refresh_BlankToken verifies that submitting an empty refreshToken
// returns 400 + ERR_AUTH_REFRESH_INVALID_INPUT + "refreshToken is required".
func TestHandler_Refresh_BlankToken(t *testing.T) {
	h, _ := setup()
	body := `{"refreshToken":""}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	h.HandleRefresh(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_AUTH_REFRESH_INVALID_INPUT", resp.Error.Code)
	assert.Equal(t, "refreshToken is required", resp.Error.Message)
}
