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
	h := NewHandler(svc)
	mux.Route(rbacRolesPrefix, func(s cell.RouteMux) {
		require.NoError(t, h.RegisterRoutes(s))
	})
	return mux
}

// uniqueRoleID returns a unique role id string safe for test isolation.
func uniqueRoleID() string { return "rs-role-" + uuid.NewString()[:8] }

// TestHandler_ListRoles_RowScope_SelfMatch: normal user listing OWN id
// (subject==id, RowScopeSelf) → 200 with the seeded role.
func TestHandler_ListRoles_RowScope_SelfMatch(t *testing.T) {
	userID := testutil.TestID("rs-list-self-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, userID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+userID, nil)
	ctx := withAllowAuthorizer(testAuthContext(userID, nil))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"RowScopeSelf matching own id must return 200")
	var resp struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data, "must return the seeded role")
}

// TestHandler_ListRoles_RowScope_SelfMismatch_IDORCollapse: normal user listing
// ANOTHER user's id (subject≠id, RowScopeSelf) → 200 but empty list (IDOR collapse).
func TestHandler_ListRoles_RowScope_SelfMismatch_IDORCollapse(t *testing.T) {
	victimID := testutil.TestID("rs-list-victim-" + uuid.NewString()[:8])
	attackerID := testutil.TestID("rs-list-attacker-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, victimID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+victimID, nil)
	// attacker (subject≠victimID) → RowScopeSelf mismatch → IDOR collapse to empty.
	ctx := withAllowAuthorizer(testAuthContext(attackerID, nil))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"RowScopeSelf mismatch must return 200 (not 403/404) with empty data")
	var resp struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Empty(t, resp.Data,
		"RowScopeSelf mismatch must IDOR-collapse to empty list, not expose victim's roles")
}

// TestHandler_ListRoles_RowScope_Admin_CrossUser: admin principal (RowScopeTenant)
// can list any user's roles → 200 with roles present.
func TestHandler_ListRoles_RowScope_Admin_CrossUser(t *testing.T) {
	targetID := testutil.TestID("rs-list-target-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, targetID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+targetID, nil)
	// admin → RowScopeTenant → no owner filter → roles returned.
	ctx := withAllowAuthorizer(testAuthContext("admin-rs", []string{auth.RoleAdmin}))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"admin (RowScopeTenant) reading another user's roles must return 200")
	var resp struct {
		Data []json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.Data,
		"admin (RowScopeTenant) must receive the seeded role, not an empty list")
}

// TestHandler_ListRoles_RowScope_SuperAdmin_FailClosed: super-admin derives
// RowScopeAll which is unsupported → 501.
func TestHandler_ListRoles_RowScope_SuperAdmin_FailClosed(t *testing.T) {
	targetID := testutil.TestID("rs-list-sa-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, targetID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+targetID, nil)
	ctx := withAllowAuthorizer(testAuthContext("superadmin-rs", []string{auth.RoleSuperAdmin}))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code,
		"super-admin (RowScopeAll) must fail-closed with 501 on accesscore role list")
}

// TestHandler_CheckRole_RowScope_SelfMatch: normal user checking own role
// (subject==id, RowScopeSelf) → 200 hasRole=true.
func TestHandler_CheckRole_RowScope_SelfMatch(t *testing.T) {
	userID := testutil.TestID("rs-check-self-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, userID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+userID+"/"+roleID, nil)
	ctx := withAllowAuthorizer(testAuthContext(userID, nil))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			HasRole bool `json:"hasRole"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.HasRole,
		"RowScopeSelf matching own id must return hasRole=true for the seeded role")
}

// TestHandler_CheckRole_RowScope_SelfMismatch_IDORCollapse: attacker checking
// victim's role (subject≠id, RowScopeSelf) → 200 hasRole=false (IDOR collapse).
func TestHandler_CheckRole_RowScope_SelfMismatch_IDORCollapse(t *testing.T) {
	victimID := testutil.TestID("rs-check-victim-" + uuid.NewString()[:8])
	attackerID := testutil.TestID("rs-check-attacker-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, victimID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+victimID+"/"+roleID, nil)
	ctx := withAllowAuthorizer(testAuthContext(attackerID, nil))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			HasRole bool `json:"hasRole"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.False(t, resp.Data.HasRole,
		"RowScopeSelf mismatch must IDOR-collapse: attacker must not see victim's role")
}

// TestHandler_CheckRole_RowScope_Admin_CrossUser: admin (RowScopeTenant) can
// check another user's role → 200 hasRole=true.
func TestHandler_CheckRole_RowScope_Admin_CrossUser(t *testing.T) {
	targetID := testutil.TestID("rs-check-target-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, targetID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+targetID+"/"+roleID, nil)
	ctx := withAllowAuthorizer(testAuthContext("admin-check-rs", []string{auth.RoleAdmin}))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data struct {
			HasRole bool `json:"hasRole"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.True(t, resp.Data.HasRole,
		"admin (RowScopeTenant) must see the seeded role for any user in tenant")
}

// TestHandler_CheckRole_RowScope_SuperAdmin_FailClosed: super-admin derives
// RowScopeAll → 501.
func TestHandler_CheckRole_RowScope_SuperAdmin_FailClosed(t *testing.T) {
	targetID := testutil.TestID("rs-check-sa-" + uuid.NewString()[:8])
	roleID := uniqueRoleID()
	r := setupRowScopeRbac(t, targetID, roleID)

	req := httptest.NewRequest(http.MethodGet, rbacRolesPrefix+"/"+targetID+"/"+roleID, nil)
	ctx := withAllowAuthorizer(testAuthContext("superadmin-check-rs", []string{auth.RoleSuperAdmin}))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code,
		"super-admin (RowScopeAll) must fail-closed with 501 on accesscore role check")
}
