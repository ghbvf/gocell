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

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
)

// testIssuer/testVerifier are declared in service_test.go

func issueRefreshToken(userID string) string {
	tok, _ := testIssuer.Issue(userID, nil, []string{"gocell"}, "sess-handler-test")
	return tok
}

func setup() (*Handler, string) {
	sessionRepo := mem.NewSessionRepository()
	refreshTok := issueRefreshToken("usr-1")

	sess, _ := domain.NewSession("usr-1", "access-tok", refreshTok, time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)

	svc := NewService(sessionRepo, mem.NewRoleRepository(), testIssuer, testVerifier, slog.Default())
	return NewHandler(svc), refreshTok
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
			body:       `{"refreshToken":"not.a.jwt"}`,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "unknown field returns 400",
			body:       `{"refreshToken":"not.a.jwt","extra":"y"}`,
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
