package rbaccheck

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

const invalidUUID = "not-a-uuid-string"

func TestRoleProjectionRows_PermissionMapping(t *testing.T) {
	roles := []*domain.Role{
		{
			ID: "r1", Name: "admin",
			Permissions: []domain.Permission{
				{Resource: "users", Action: "read"},
				{Resource: "orders", Action: "write"},
			},
		},
	}
	rows, err := toRoleProjectionRows(roles)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// Round-trip through JSON to verify wire keys (camelCase, #27n).
	b, marshalErr := json.Marshal(rows[0])
	require.NoError(t, marshalErr)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))

	assert.Equal(t, "r1", m["id"])
	assert.Equal(t, "admin", m["name"])
	perms, ok := m["permissions"].([]any)
	require.True(t, ok)
	require.Len(t, perms, 2)
	p0 := perms[0].(map[string]any)
	assert.Equal(t, "users", p0["resource"])
	assert.Equal(t, "read", p0["action"])
	p1 := perms[1].(map[string]any)
	assert.Equal(t, "orders", p1["resource"])
	assert.Equal(t, "write", p1["action"])
}

func TestRoleProjectionRows_EmptyPermissions(t *testing.T) {
	roles := []*domain.Role{{ID: "r2", Name: "viewer", Permissions: nil}}
	rows, err := toRoleProjectionRows(roles)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	b, marshalErr := json.Marshal(rows[0])
	require.NoError(t, marshalErr)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	perms, ok := m["permissions"].([]any)
	require.True(t, ok, "permissions key must be present")
	assert.Empty(t, perms)
}

func setup(t *testing.T, runMode query.RunMode) http.Handler {
	t.Helper()
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	roleRepo.SeedRole(testTenantID, &domain.Role{
		ID: "r1", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
			{Resource: "users", Action: "write"},
		},
	})
	roleRepo.SeedUserRoleAssignment(testTenantID, testutil.TestID("user-1"), "r1")

	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		panic(err)
	}
	svc, err := NewService(roleRepo, codec, slog.Default(), runMode,
		WithTxManager(outbox.DemoCellTxManager()))
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
				req = req.WithContext(testAuthContext(tc.subject, tc.roles))
			}
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestHandleList_ExceedsMaxLimit(t *testing.T) {
	r := setup(t, query.RunModeProd)
	w := httptest.NewRecorder()
	// limit=501 exceeds the 500-item ceiling enforced by httputil.ParsePageParams
	// (F4 absorb: generated handler routes cursor/limit through ParsePageParams,
	// which returns ERR_PAGE_SIZE_EXCEEDED for limit > 500).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+testutil.TestID("user-1")+"?limit=501", nil)
	req = req.WithContext(testAuthContext(testutil.TestID("user-1"), nil))

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandler_ListRoles_ProdMode_InvalidCursor_Returns400(t *testing.T) {
	r := setup(t, query.RunModeProd)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+testutil.TestID("user-1")+"?cursor=not-a-valid-cursor", nil)
	req = req.WithContext(testAuthContext(testutil.TestID("user-1"), nil))

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
