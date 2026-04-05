package sessionlogin

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// testIssuer is declared in service_test.go

func setup() http.Handler {
	userRepo := mem.NewUserRepository()
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct-pass"), bcrypt.MinCost)
	user := &domain.User{
		ID: "usr-1", Username: "alice", Email: "a@b.com",
		PasswordHash: string(hash), Status: domain.StatusActive,
	}
	_ = userRepo.Create(context.Background(), user)

	svc := NewService(userRepo, mem.NewSessionRepository(), mem.NewRoleRepository(), eventbus.New(), testIssuer, slog.Default())
	r := chi.NewRouter()
	r.Post("/login", NewHandler(svc).HandleLogin)
	return r
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
			name:       "wrong password returns 401",
			body:       `{"username":"alice","password":"wrong"}`,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}
