package rbacassign

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
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

func setupHandler(t *testing.T) (http.Handler, *mem.Store) {
	t.Helper()
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	})
	// Seed usr-1 as an effective admin so the default Revoke test scenarios
	// have a real admin to operate on. Additional active admins must be added
	// by test setup hooks for the multi-holder cases.
	u1, u1Err := domain.NewUser("usr-1", "usr-1@test.local", "$2a$12$hash", time.Now())
	require.NoError(t, u1Err)
	u1.ID = "usr-1"
	require.NoError(t, store.UserRepository().Create(context.Background(), u1))
	_, err := store.RoleRepository().AssignToUser(context.Background(), "usr-1", "admin")
	require.NoError(t, err)

	svc := mustNewService(t, store.RoleRepository(), store.UserRepository(), testutil.RealSessionRepo(t), slog.Default())
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/internal/v1/access/roles", func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			panic("setupHandler: RegisterRoutes: " + err.Error())
		}
	})
	return mux, store
}

// seedActiveAdminInStore is the handler-test analog of assignActiveAdmin
// (service_test.go) that operates on the store returned by setupHandler.
func seedActiveAdminInStore(t *testing.T, store *mem.Store, userID string) {
	t.Helper()
	u, err := domain.NewUser(userID, userID+"@test.local", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	u.ID = userID
	require.NoError(t, store.UserRepository().Create(context.Background(), u))
	_, err = store.RoleRepository().AssignToUser(context.Background(), userID, "admin")
	require.NoError(t, err)
}

func TestHandler_Assign(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		ctx        func() context.Context // nil = no auth
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			// Spec: accesscore caller (PrincipalService, CallerCellID=accesscore) → 201
			name:       "accesscore caller assigns role returns 201",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
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
			ctx:        func() context.Context { return auth.TestContext("usr-2", []string{"viewer"}) },
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no auth returns 401",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			ctx:        nil, // no auth
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "invalid body returns 400",
			body:       `{bad json`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty userId returns 400",
			body:       `{"userId":"","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "role not found returns 404",
			body:       `{"userId":"usr-2","roleId":"nonexistent"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusNotFound,
		},
		{
			// Spec: caller='configcore' not in allowlist → 403
			name:       "configcore caller not in allowlist returns 403",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("configcore") },
			wantStatus: http.StatusForbidden,
		},
		{
			// Spec: empty callerCellID → 403
			name:       "empty caller returns 403",
			body:       `{"userId":"usr-2","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("") },
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := setupHandler(t)
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/assign", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.ctx != nil {
				req = req.WithContext(tc.ctx())
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
		setup      func(*testing.T, *mem.Store) // extra setup before request
		body       string
		ctx        func() context.Context // nil = no auth
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name: "accesscore caller revokes role returns 200 (multiple holders)",
			setup: func(t *testing.T, s *mem.Store) {
				// Ensure 2 effective admins so last-admin guard doesn't block.
				seedActiveAdminInStore(t, s, "usr-2")
			},
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
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
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "invalid body returns 400",
			body:       `{bad json`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty userId returns 400",
			body:       `{"userId":"","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "empty roleId returns 400",
			body:       `{"userId":"usr-1","roleId":""}`,
			ctx:        func() context.Context { return auth.TestServiceContext("accesscore") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "non-admin returns 403",
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestContext("usr-2", []string{"viewer"}) },
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "non-allowlisted caller returns 403",
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			ctx:        func() context.Context { return auth.TestServiceContext("configcore") },
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no auth returns 401",
			body:       `{"userId":"usr-1","roleId":"admin"}`,
			ctx:        nil, // no auth
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, store := setupHandler(t)
			if tc.setup != nil {
				tc.setup(t, store)
			}
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/access/roles/revoke", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			if tc.ctx != nil {
				req = req.WithContext(tc.ctx())
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
