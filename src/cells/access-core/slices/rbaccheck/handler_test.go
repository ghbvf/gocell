package rbaccheck

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
)

func setup() http.Handler {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{ID: "r1", Name: "admin"})
	_ = roleRepo.AssignToUser(context.Background(), "user-1", "r1")

	svc := NewService(roleRepo, slog.Default())
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "GET /{userID} returns roles",
			path:       "/user-1",
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data  []json.RawMessage `json:"data"`
					Total int               `json:"total"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Equal(t, 1, resp.Total)
			},
		},
		{
			name:       "GET /{userID} no roles returns empty",
			path:       "/unknown-user",
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Total int `json:"total"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Equal(t, 0, resp.Total)
			},
		},
		{
			name:       "GET /{userID}/{roleName} has role",
			path:       "/user-1/admin",
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data struct {
						HasRole bool `json:"hasRole"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.True(t, resp.Data.HasRole)
			},
		},
		{
			name:       "GET /{userID}/{roleName} missing role",
			path:       "/user-1/viewer",
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data struct {
						HasRole bool `json:"hasRole"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.False(t, resp.Data.HasRole)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup()
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}
