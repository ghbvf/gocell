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

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
)

// testKey is declared in service_test.go

func issueRefreshToken(userID string) string {
	claims := jwt.MapClaims{
		"sub": userID,
		"exp": jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour)),
		"iat": jwt.NewNumericDate(time.Now()),
		"iss": "gocell-access-core",
		"aud": jwt.ClaimStrings{"gocell"},
	}
	tok, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(testKey)
	return tok
}

func setup() (http.Handler, string) {
	sessionRepo := mem.NewSessionRepository()
	refreshTok := issueRefreshToken("usr-1")

	sess, _ := domain.NewSession("usr-1", "access-tok", refreshTok, time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)

	svc := NewService(sessionRepo, mem.NewRoleRepository(), testKey, slog.Default())
	r := chi.NewRouter()
	r.Post("/refresh", NewHandler(svc).HandleRefresh)
	return r, refreshTok
}

func TestHandleRefresh(t *testing.T) {
	router, validToken := setup()

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
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.NotEmpty(t, resp.Data.AccessToken)
				assert.NotEmpty(t, resp.Data.RefreshToken)
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/refresh", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}
