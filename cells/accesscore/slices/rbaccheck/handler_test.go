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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

const invalidUUID = "not-a-uuid-string"

func TestRoleResponse_PermissionMapping(t *testing.T) {
	role := &domain.Role{
		ID: "r1", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
			{Resource: "orders", Action: "write"},
		},
	}
	resp := toRoleResponse(role)

	assert.Equal(t, "r1", resp.ID)
	assert.Equal(t, "admin", resp.Name)
	require.Len(t, resp.Permissions, 2)
	assert.Equal(t, "users", resp.Permissions[0].Resource)
	assert.Equal(t, "read", resp.Permissions[0].Action)
	assert.Equal(t, "orders", resp.Permissions[1].Resource)
	assert.Equal(t, "write", resp.Permissions[1].Action)

	// Verify camelCase JSON keys (#27n).
	b, err := json.Marshal(resp)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"id"`)
	assert.Contains(t, s, `"name"`)
	assert.Contains(t, s, `"permissions"`)
	assert.Contains(t, s, `"resource"`)
	assert.Contains(t, s, `"action"`)
}

func TestRoleResponse_EmptyPermissions(t *testing.T) {
	role := &domain.Role{ID: "r2", Name: "viewer", Permissions: nil}
	resp := toRoleResponse(role)
	assert.Empty(t, resp.Permissions)
}

func setup(t *testing.T, runMode query.RunMode) http.Handler {
	t.Helper()
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "r1", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
			{Resource: "users", Action: "write"},
		},
	})
	_, _ = roleRepo.AssignToUser(context.Background(), testutil.TestID("user-1"), "r1")

	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		panic(err)
	}
	svc, err := NewService(roleRepo, codec, slog.Default(), runMode)
	if err != nil {
		panic(err)
	}
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/api/v1/access/roles", func(s cell.RouteMux) {
		require.NoError(t, h.RegisterRoutes(s))
	})
	return mux
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		subject    string
		roles      []string
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		{
			name:       "GET /{userID} self-access returns roles with permissions",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-1"),
			roles:      nil,
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data []struct {
						ID          string `json:"id"`
						Name        string `json:"name"`
						Permissions []struct {
							Resource string `json:"resource"`
							Action   string `json:"action"`
						} `json:"permissions"`
					} `json:"data"`
					HasMore bool `json:"hasMore"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				require.Len(t, resp.Data, 1)
				assert.Equal(t, "admin", resp.Data[0].Name)
				require.Len(t, resp.Data[0].Permissions, 2)
				assert.Equal(t, "users", resp.Data[0].Permissions[0].Resource)
				assert.Equal(t, "read", resp.Data[0].Permissions[0].Action)
				assert.False(t, resp.HasMore)
			},
		},
		{
			name:       "GET /{userID} self-access no roles returns empty",
			path:       "/api/v1/access/roles/" + testutil.TestID("unknown-user"),
			subject:    testutil.TestID("unknown-user"),
			wantStatus: http.StatusOK,
			checkBody: func(t *testing.T, body []byte) {
				var resp struct {
					Data []json.RawMessage `json:"data"`
				}
				require.NoError(t, json.Unmarshal(body, &resp))
				assert.Empty(t, resp.Data)
			},
		},
		{
			name:       "GET /{userID}/{roleName} self-access has role",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1") + "/admin",
			subject:    testutil.TestID("user-1"),
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
			name:       "GET /{userID}/{roleName} self-access missing role",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1") + "/viewer",
			subject:    testutil.TestID("user-1"),
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
		// Trust boundary tests (#27r)
		{
			name:       "GET /{userID} admin bypass allowed",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("admin-user"),
			roles:      []string{"admin"},
			wantStatus: http.StatusOK,
		},
		{
			name:       "GET /{userID} different user no admin returns 403",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-2"),
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{userID}/{roleName} different user no admin returns 403",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1") + "/admin",
			subject:    testutil.TestID("user-2"),
			roles:      []string{"viewer"},
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{userID} no subject returns 401",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    "", // no auth context
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "GET /{userID} invalid UUID returns 400",
			path:       "/api/v1/access/roles/" + invalidUUID,
			subject:    testutil.TestID("user-1"),
			roles:      []string{"admin"},
			wantStatus: http.StatusBadRequest,
			checkBody: func(t *testing.T, body []byte) {
				var b struct {
					Error struct {
						Code string `json:"code"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(body, &b))
				assert.Equal(t, string(errcode.ErrValidationInvalidUUID), b.Error.Code)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := setup(t, query.RunModeDemo)
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.subject != "" {
				req = req.WithContext(auth.TestContext(tc.subject, tc.roles))
			}
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandler_ListRoles_ProdMode_InvalidCursor_Returns400(t *testing.T) {
	r := setup(t, query.RunModeProd)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+testutil.TestID("user-1")+"?cursor=not-a-valid-cursor", nil)
	req = req.WithContext(auth.TestContext(testutil.TestID("user-1"), nil))

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
