package rbaccheck

// handler_rowscope_test.go — #1709 RowVisibility obligation handler-level tests.
//
// These tests prove the route-gate-passed-but-RowScope-collapses vector for the
// GET /api/v1/access/roles/{userID} (list) and
// GET /api/v1/access/roles/{userID}/{roleName} (check) adapters:
//   - normal user (RowScopeSelf, subject=own id) → roles returned / hasRole checked
//   - normal user (RowScopeSelf, subject≠id) → 200 with empty list / hasRole=false
//   - admin principal (RowScopeTenant) → full result even for another user's id
//   - super-admin (RowScopeAll) → 501 (RowScopeAllUnsupportedError)
//
// The PDP gate is covered by handler_test.go; here we wire an allow Authorizer
// so the gate is not the bottleneck and we isolate the data-layer row-scope.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

const rbacRolesPrefix = "/api/v1/access/roles"

// setupRowScopeRbac builds a handler with a seeded role and user-role assignment.
// Returns the handler.
func setupRowScopeRbac(t *testing.T, userID, roleID string) http.Handler {
	t.Helper()
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	roleRepo.SeedRole(testTenantID, &domain.Role{
		ID:   roleID,
		Name: roleID,
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
		},
	})
	// Seed the assignment so the user holds the role.
	roleRepo.SeedUserRoleAssignment(testTenantID, userID, roleID)

	codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
	if err != nil {
		t.Fatalf("setupRowScopeRbac: codec: %v", err)
	}
	svc, err := NewService(roleRepo, codec, slog.Default(), query.RunModeDemo,
		WithTxManager(outbox.DemoCellTxManager()))
	if err != nil {
		t.Fatalf("setupRowScopeRbac: NewService: %v", err)
	}
	mux := celltest.NewTestMux()
	h := NewHandler(svc, testResolver())
	mux.Route(rbacRolesPrefix, func(s cell.RouteMux) {
		require.NoError(t, h.RegisterRoutes(s))
	})
	return mux
}

// uniqueRoleID returns a unique role id string safe for test isolation.
func uniqueRoleID() string { return "rs-role-" + uuid.NewString()[:8] }

// rbacRowScopeCase describes one scenario for a single RBAC endpoint × RowScope test.
type rbacRowScopeCase struct {
	name string
	// buildPrincipalID returns the auth-context subject for this scenario.
	// It receives the targetUserID so self-match cases can use it directly.
	buildPrincipalID func(targetUserID string) string
	buildRoles       func() []string
	wantStatus       int
}

// listRolesRowScopeCases defines the 4 RowScope scenarios for GET /roles/{userID}.
func listRolesRowScopeCases() []rbacRowScopeCase {
	return []rbacRowScopeCase{
		{
			name:             "SelfMatch",
			buildPrincipalID: func(targetID string) string { return targetID },
			wantStatus:       http.StatusOK,
		},
		{
			name:             "SelfMismatch_IDORCollapse",
			buildPrincipalID: func(_ string) string { return testutil.TestID("rs-list-attacker-" + uuid.NewString()[:8]) },
			wantStatus:       http.StatusOK,
		},
		{
			name:             "Admin_CrossUser",
			buildPrincipalID: func(_ string) string { return "admin-rs" },
			buildRoles:       func() []string { return []string{auth.RoleAdmin} },
			wantStatus:       http.StatusOK,
		},
		{
			name:             "SuperAdmin_FailClosed",
			buildPrincipalID: func(_ string) string { return "superadmin-rs" },
			buildRoles:       func() []string { return []string{auth.RoleSuperAdmin} },
			wantStatus:       http.StatusNotImplemented,
		},
	}
}

// TestHandler_ListRoles_RowScope runs all 4 RowScope scenarios for GET /roles/{userID}
// in a single table-driven test (#1709).
func TestHandler_ListRoles_RowScope(t *testing.T) {
	for _, tc := range listRolesRowScopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			targetID := testutil.TestID("rs-list-" + uuid.NewString()[:8])
			roleID := uniqueRoleID()
			r := setupRowScopeRbac(t, targetID, roleID)

			req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+targetID, nil)
			var roles []string
			if tc.buildRoles != nil {
				roles = tc.buildRoles()
			}
			ctx := withAllowAuthorizer(testAuthContext(tc.buildPrincipalID(targetID), roles))
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code, "unexpected status for case %s", tc.name)
			if tc.wantStatus == http.StatusOK {
				var resp struct {
					Data []json.RawMessage `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				switch tc.name {
				case "SelfMatch", "Admin_CrossUser":
					assert.NotEmpty(t, resp.Data, "case %s: must return the seeded role", tc.name)
				case "SelfMismatch_IDORCollapse":
					assert.Empty(t, resp.Data,
						"RowScopeSelf mismatch must IDOR-collapse to empty list")
				}
			}
		})
	}
}

// checkRoleRowScopeCases defines the 4 RowScope scenarios for GET /roles/{userID}/{roleName}.
func checkRoleRowScopeCases() []rbacRowScopeCase {
	return []rbacRowScopeCase{
		{
			name:             "SelfMatch",
			buildPrincipalID: func(targetID string) string { return targetID },
			wantStatus:       http.StatusOK,
		},
		{
			name:             "SelfMismatch_IDORCollapse",
			buildPrincipalID: func(_ string) string { return testutil.TestID("rs-check-attacker-" + uuid.NewString()[:8]) },
			wantStatus:       http.StatusOK,
		},
		{
			name:             "Admin_CrossUser",
			buildPrincipalID: func(_ string) string { return "admin-check-rs" },
			buildRoles:       func() []string { return []string{auth.RoleAdmin} },
			wantStatus:       http.StatusOK,
		},
		{
			name:             "SuperAdmin_FailClosed",
			buildPrincipalID: func(_ string) string { return "superadmin-check-rs" },
			buildRoles:       func() []string { return []string{auth.RoleSuperAdmin} },
			wantStatus:       http.StatusNotImplemented,
		},
	}
}

// TestHandler_CheckRole_RowScope runs all 4 RowScope scenarios for GET /roles/{userID}/{roleName}
// in a single table-driven test (#1709).
func TestHandler_CheckRole_RowScope(t *testing.T) {
	for _, tc := range checkRoleRowScopeCases() {
		t.Run(tc.name, func(t *testing.T) {
			targetID := testutil.TestID("rs-check-" + uuid.NewString()[:8])
			roleID := uniqueRoleID()
			r := setupRowScopeRbac(t, targetID, roleID)

			req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+targetID+"/"+roleID, nil)
			var roles []string
			if tc.buildRoles != nil {
				roles = tc.buildRoles()
			}
			ctx := withAllowAuthorizer(testAuthContext(tc.buildPrincipalID(targetID), roles))
			req = req.WithContext(ctx)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code, "unexpected status for case %s", tc.name)
			if tc.wantStatus == http.StatusOK {
				var resp struct {
					Data struct {
						HasRole bool `json:"hasRole"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				switch tc.name {
				case "SelfMatch", "Admin_CrossUser":
					assert.True(t, resp.Data.HasRole,
						"case %s: must see the seeded role", tc.name)
				case "SelfMismatch_IDORCollapse":
					assert.False(t, resp.Data.HasRole,
						"RowScopeSelf mismatch must IDOR-collapse: attacker must not see victim's role")
				}
			}
		})
	}
}
