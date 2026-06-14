package identitymanage

// handler_rowscope_test.go — #1709 RowVisibility obligation handler-level tests.
//
// These tests prove the route-gate-passed-but-RowScope-collapses vector for the
// GET /api/v1/access/users/{id} adapter:
//   - normal user (RowScopeSelf, subject=own id) reading ANOTHER user's id → 404
//   - normal user (RowScopeSelf, subject=own id) reading OWN id → 200
//   - admin principal (RowScopeTenant) → 200 even for another user's id
//   - super-admin (RowScopeAll) → 501 (RowScopeAllUnsupportedError, KindNotImplemented)
//
// The PDP gate (RequirePermissionForResource) is already covered by handler_test.go.
// Here we wire a fresh handler stack with a mem repo, seed a user, and verify the
// data-layer row-scope collapse independent of the authz gate (we inject an allow
// Authorizer so the gate is not the bottleneck).

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/framework/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh/storetest"
)

// rowScopeAPIPrefix mirrors the canonical prefix from handler_test.go.
const rowScopeAPIPrefix = "/api/v1/access/users"

// setupRowScopeHandler builds a handler + seeded mem repo for row-scope tests.
// It returns the handler and the concrete repo so tests can seed directly.
func setupRowScopeHandler(t *testing.T) (http.Handler, *mem.UserRepository) {
	t.Helper()
	repo := mem.NewStore(clock.Real()).UserRepository()
	clk := storetest.NewFakeClock(time.Now())
	rs, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  2 * time.Second,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		t.Fatalf("setupRowScopeHandler: refreshmem.New: %v", err)
	}
	sessionStore := testutil.RealSessionRepo(t)
	svc, err := NewService(clock.Real(), repo,
		newInvalidator(t, repo, sessionStore, rs),
		slog.Default(),
		inertRoleRepo(),
		WithTokenIssuer(handlerStubIssuer),
		WithTxManager(persistence.WrapForCell(contractTxRunner{})),
	)
	if err != nil {
		t.Fatalf("setupRowScopeHandler: NewService: %v", err)
	}
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route(rowScopeAPIPrefix, func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			t.Fatalf("setupRowScopeHandler: RegisterRoutes: %v", err)
		}
	})
	return mux, repo
}

// seedRowScopeUser creates a user with a known id and bcrypt hash directly in
// the given mem repo.
func seedRowScopeUser(t *testing.T, repo *mem.UserRepository, id, username string) {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("pass1234"), bcrypt.MinCost)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Millisecond)
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           id,
		Username:     username,
		Email:        username + "@rowscope.test",
		PasswordHash: string(hash),
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	require.NoError(t, err)
	// Use canonical test tenant UUID matching handler_test.go's testTenantID.
	require.NoError(t, repo.Create(context.Background(), testTenantID, u))
}

// withRowScopeTenant injects the test tenant into a context.
func withRowScopeTenant(ctx context.Context) context.Context {
	return ctxkeys.WithTenantID(ctx, "00000000-0000-0000-0000-000000000001")
}

// TestHandler_GetUser_RowScope_SelfMatch: normal user reading their OWN id
// (RowScopeSelf, subject==id) → 200 with the user data.
func TestHandler_GetUser_RowScope_SelfMatch(t *testing.T) {
	r, repo := setupRowScopeHandler(t)
	userID := testutil.TestID("rs-self-match-" + uuid.NewString()[:8])
	seedRowScopeUser(t, repo, userID, "rs_self_match")

	req := httptest.NewRequest(http.MethodGet, rowScopeAPIPrefix+"/"+userID, nil)
	// Subject == userID → RowScopeSelf, subject matches → row returned.
	ctx := withAllowAuthorizer(withRowScopeTenant(auth.TestContext(userID, nil)))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code, "self-access matching own id must return 200")
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, string(body["data"]), userID)
}

// TestHandler_GetUser_RowScope_SelfMismatch_IDORCollapse: normal user reading
// ANOTHER user's id (RowScopeSelf, subject≠id) → 404 (IDOR collapse, same
// envelope as a genuine not-found so the caller cannot probe existence).
func TestHandler_GetUser_RowScope_SelfMismatch_IDORCollapse(t *testing.T) {
	r, repo := setupRowScopeHandler(t)
	victimID := testutil.TestID("rs-victim-" + uuid.NewString()[:8])
	attackerID := testutil.TestID("rs-attacker-" + uuid.NewString()[:8])
	seedRowScopeUser(t, repo, victimID, "rs_victim")

	req := httptest.NewRequest(http.MethodGet, rowScopeAPIPrefix+"/"+victimID, nil)
	// Subject == attackerID ≠ victimID → RowScopeSelf, subject mismatch → IDOR collapse.
	ctx := withAllowAuthorizer(withRowScopeTenant(auth.TestContext(attackerID, nil)))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code,
		"RowScopeSelf with non-matching subject must IDOR-collapse to 404")
}

// TestHandler_GetUser_RowScope_Admin_CrossUser: admin principal (RowScopeTenant)
// can read any user in the tenant → 200.
func TestHandler_GetUser_RowScope_Admin_CrossUser(t *testing.T) {
	r, repo := setupRowScopeHandler(t)
	targetID := testutil.TestID("rs-target-" + uuid.NewString()[:8])
	seedRowScopeUser(t, repo, targetID, "rs_target")

	req := httptest.NewRequest(http.MethodGet, rowScopeAPIPrefix+"/"+targetID, nil)
	// Admin role → RowScopeTenant (no owner filter) → row returned.
	ctx := withAllowAuthorizer(withRowScopeTenant(auth.TestContext("admin-user-rs", []string{auth.RoleAdmin})))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code,
		"admin principal (RowScopeTenant) reading another user must return 200")
	var body map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Contains(t, string(body["data"]), targetID)
}

// TestHandler_GetUser_RowScope_SuperAdmin_FailClosed: super-admin principal
// derives RowScopeAll, which accesscore does not support → 501.
func TestHandler_GetUser_RowScope_SuperAdmin_FailClosed(t *testing.T) {
	r, repo := setupRowScopeHandler(t)
	targetID := testutil.TestID("rs-superadmin-" + uuid.NewString()[:8])
	seedRowScopeUser(t, repo, targetID, "rs_superadmin_target")

	req := httptest.NewRequest(http.MethodGet, rowScopeAPIPrefix+"/"+targetID, nil)
	// super-admin role → RowScopeAll → RowScopeAllUnsupportedError → 501.
	ctx := withAllowAuthorizer(withRowScopeTenant(auth.TestContext("superadmin-user", []string{auth.RoleSuperAdmin})))
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotImplemented, w.Code,
		"super-admin (RowScopeAll) must fail-closed with 501 on accesscore user read")
}
