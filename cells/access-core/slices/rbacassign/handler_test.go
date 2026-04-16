package rbacassign

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/runtime/auth"
)

func setupHandler() (http.Handler, *mem.RoleRepository) {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	})
	_ = roleRepo.AssignToUser(context.Background(), "usr-1", "admin")

	svc := NewService(roleRepo, mem.NewSessionRepository(), slog.Default())
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)
	return mux, roleRepo
}

func TestHandler_Assign(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		subject    string
		roles      []string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "admin assigns role returns 201",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			subject:    "usr-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusCreated,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data struct {
						UserID   string `json:"userId"`
						RoleID   string `json:"roleId"`
						Assigned bool   `json:"assigned"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Equal(t, "usr-2", resp.Data.UserID)
				assert.Equal(t, "admin", resp.Data.RoleID)
				assert.True(t, resp.Data.Assigned)
			},
		},
		{
			name:       "non-admin returns 403",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			subject:    "usr-2",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no auth returns 401",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			subject:    "",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid body returns 400",
			body:       `{bad json`,
			subject:    "usr-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty userId returns 400",
			body:       `{"userId":"","roleId":"admin"}`,
			subject:    "usr-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "role not found returns 404",
			body:       `{"userId":"usr-2","roleId":"nonexistent"}`,
			subject:    "usr-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := setupHandler()
			req := httptest.NewRequest(http.MethodPost, "/assign", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandler_Revoke(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(*mem.RoleRepository) // extra setup before request
		body       string
		subject    string
		roles      []string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name: "admin revokes role returns 200 (multiple holders)",
			setup: func(r *mem.RoleRepository) {
				// Ensure 2 admins so last-admin guard doesn't block.
				_ = r.AssignToUser(context.Background(), "usr-2", "admin")
			},
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			subject:    "usr-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data struct {
						UserID  string `json:"userId"`
						RoleID  string `json:"roleId"`
						Revoked bool   `json:"revoked"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Equal(t, "usr-1", resp.Data.UserID)
				assert.Equal(t, "admin", resp.Data.RoleID)
				assert.True(t, resp.Data.Revoked)
			},
		},
		{
			name:       "revoke last admin returns 403",
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			subject:    "usr-1",
			roles:      []string{"admin"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "non-admin returns 403",
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			subject:    "usr-2",
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no auth returns 401",
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			subject:    "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, roleRepo := setupHandler()
			if tc.setup != nil {
				tc.setup(roleRepo)
			}
			req := httptest.NewRequest(http.MethodPost, "/revoke", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}
