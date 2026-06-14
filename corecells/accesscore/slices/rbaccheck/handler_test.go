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

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// rbacTestAuthorizer is a local test double for auth.Authorizer. It cannot
// live in accesscoretest because that package transitively imports corecells/accesscore
// (hasher.go), which imports this slice — creating a cycle. The verdict
// construction (authz.Allow/Deny) is in _test.go, sanctioned by
// AUTHZ-DECISION-ALLOW-DENY-CALLER-01.
type rbacTestAuthorizer struct {
	decision authz.Decision
}

func (a *rbacTestAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return a.decision, nil
}

// allowAuthorizer / withAllowAuthorizer / withDenyAuthorizer build the PDP
// verdicts for tests. authz.Allow/Deny construction lives here in _test.go.
func allowAuthorizer() *rbacTestAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test allowAuthorizer: authz.Allow: " + err.Error())
	}
	return &rbacTestAuthorizer{decision: dec}
}

func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, allowAuthorizer())
}

func withDenyAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, &rbacTestAuthorizer{decision: authz.Deny("test: denied")})
}

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
		name    string
		path    string
		subject string
		roles   []string
		// ctxFn optionally enriches the auth context (e.g. inject an Authorizer for
		// the non-self PDP path). Applied after testAuthContext when subject != "".
		ctxFn      func(context.Context) context.Context
		wantStatus int
		checkBody  func(t *testing.T, body []byte)
	}{
		// Self-access: subject == userID path param → PDP baseline ownership rule fires.
		// A wired Authorizer is required (#1977 Batch B: self is now PDP-decided).
		{
			name:       "GET /{userID} self-access returns roles with permissions",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-1"),
			roles:      nil,
			ctxFn:      withAllowAuthorizer,
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
			ctxFn:      withAllowAuthorizer,
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
			ctxFn:      withAllowAuthorizer,
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
			ctxFn:      withAllowAuthorizer,
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
		// Non-self PDP path: subject != userID → RequirePermission(PermRoleRead()) consulted.
		// Admin reading another user: allow Authorizer → 200 (PDP allows).
		{
			name:       "GET /{userID} admin PDP allow reads another user",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("admin-user"),
			roles:      []string{"admin"},
			ctxFn:      withAllowAuthorizer,
			wantStatus: http.StatusOK,
		},
		// Non-self, no Authorizer: fail-closed → 403 (gate cannot consult PDP).
		{
			name:       "GET /{userID} non-self no Authorizer fail-closed 403",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("admin-user"),
			roles:      []string{"admin"},
			wantStatus: http.StatusForbidden,
		},
		// Non-self, PDP deny: viewer reading another user → 403.
		{
			name:       "GET /{userID} different user PDP deny returns 403",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    testutil.TestID("user-2"),
			roles:      []string{"viewer"},
			ctxFn:      withDenyAuthorizer,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{userID}/{roleName} different user PDP deny returns 403",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1") + "/admin",
			subject:    testutil.TestID("user-2"),
			roles:      []string{"viewer"},
			ctxFn:      withDenyAuthorizer,
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "GET /{userID} no subject returns 401",
			path:       "/api/v1/access/roles/" + testutil.TestID("user-1"),
			subject:    "", // no auth context
			wantStatus: http.StatusUnauthorized,
		},
		{
			// The invalid UUID is not self (cannot match subject UUID) and the path is
			// reached for auth check. We inject an allow Authorizer so the gate passes
			// and the generated handler's UUID validator fires the 400.
			name:       "GET /{userID} invalid UUID returns 400",
			path:       "/api/v1/access/roles/" + invalidUUID,
			subject:    testutil.TestID("user-1"),
			ctxFn:      withAllowAuthorizer,
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
				ctx := testAuthContext(tc.subject, tc.roles)
				if tc.ctxFn != nil {
					ctx = tc.ctxFn(ctx)
				}
				req = req.WithContext(ctx)
			}
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
			if tc.checkBody != nil {
				tc.checkBody(t, w.Body.Bytes())
			}
		})
	}
}

// TestHandler_SelfRequiresAuthorizer proves that param==subject now requires a
// wired Authorizer (#1977 Batch B: self is PDP-decided via baseline ownership
// rule subject.sub == resource.id; RequirePermissionOrSelf removed). Without a
// wired Authorizer the gate fails closed (403), not 200.
func TestHandler_SelfRequiresAuthorizer(t *testing.T) {
	r := setup(t, query.RunModeDemo)
	// Build a context with a subject and tenant but deliberately NO Authorizer.
	ctx := testAuthContext(testutil.TestID("user-1"), nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+testutil.TestID("user-1"), nil)
	req = req.WithContext(ctx)
	r.ServeHTTP(w, req)
	// Without Authorizer, RequirePermissionForResource fails closed (403).
	assert.Equal(t, http.StatusForbidden, w.Code,
		"self-access without Authorizer must fail-closed (403) after #1977 Batch B; "+
			"self is now PDP-decided, not a Go short-circuit")
}

func TestHandleList_ExceedsMaxLimit(t *testing.T) {
	r := setup(t, query.RunModeProd)
	w := httptest.NewRecorder()
	// limit=501 exceeds the 500-item ceiling enforced by httputil.ParsePageParams
	// (F4 absorb: generated handler routes cursor/limit through ParsePageParams,
	// which returns ERR_PAGE_SIZE_EXCEEDED for limit > 500).
	// Self-access requires a wired Authorizer (#1977 Batch B).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+testutil.TestID("user-1")+"?limit=501", nil)
	req = req.WithContext(withAllowAuthorizer(testAuthContext(testutil.TestID("user-1"), nil)))

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "ERR_PAGE_SIZE_EXCEEDED")
}

func TestHandler_ListRoles_ProdMode_InvalidCursor_Returns400(t *testing.T) {
	r := setup(t, query.RunModeProd)
	w := httptest.NewRecorder()
	// Self-access requires a wired Authorizer (#1977 Batch B).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/roles/"+testutil.TestID("user-1")+"?cursor=not-a-valid-cursor", nil)
	req = req.WithContext(withAllowAuthorizer(testAuthContext(testutil.TestID("user-1"), nil)))

	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}
