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

// getUserRowScopeCase describes one scenario for the GET /users/{id} RowScope matrix.
type getUserRowScopeCase struct {
	name       string
	buildCtx   func(targetID string) context.Context
	wantStatus int
	// wantIDInBody: if true, assert that the response body contains the targetID.
	wantIDInBody bool
}

// TestHandler_GetUser_RowScope runs all 4 RowScope scenarios for GET /users/{id}
// in a single table-driven test (#1709).
func TestHandler_GetUser_RowScope(t *testing.T) {
	cases := []getUserRowScopeCase{
		{
			name: "SelfMatch",
			// Subject == targetID → RowScopeSelf, subject matches → row returned.
			buildCtx: func(targetID string) context.Context {
				return withAllowAuthorizer(withRowScopeTenant(auth.TestContext(targetID, nil)))
			},
			wantStatus:   http.StatusOK,
			wantIDInBody: true,
		},
		{
			name: "SelfMismatch_IDORCollapse",
			// Subject == attackerID ≠ targetID → RowScopeSelf, subject mismatch → IDOR collapse.
			buildCtx: func(_ string) context.Context {
				return withAllowAuthorizer(withRowScopeTenant(
					auth.TestContext(testutil.TestID("rs-attacker-"+uuid.NewString()[:8]), nil),
				))
			},
			wantStatus:   http.StatusNotFound,
			wantIDInBody: false,
		},
		{
			name: "Admin_CrossUser",
			// Admin role → RowScopeTenant (no owner filter) → row returned.
			buildCtx: func(_ string) context.Context {
				return withAllowAuthorizer(withRowScopeTenant(
					auth.TestContext("admin-user-rs", []string{auth.RoleAdmin}),
				))
			},
			wantStatus:   http.StatusOK,
			wantIDInBody: true,
		},
		{
			name: "SuperAdmin_FailClosed",
			// super-admin role → RowScopeAll → RowScopeAllUnsupportedError → 501.
			buildCtx: func(_ string) context.Context {
				return withAllowAuthorizer(withRowScopeTenant(
					auth.TestContext("superadmin-user", []string{auth.RoleSuperAdmin}),
				))
			},
			wantStatus:   http.StatusNotImplemented,
			wantIDInBody: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, repo := setupRowScopeHandler(t)
			targetID := testutil.TestID("rs-" + uuid.NewString()[:8])
			seedRowScopeUser(t, repo, targetID, "rs_user_"+tc.name)

			req := httptest.NewRequest(http.MethodGet, rowScopeAPIPrefix+"/"+targetID, nil)
			req = req.WithContext(tc.buildCtx(targetID))

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			assert.Equal(t, tc.wantStatus, w.Code, "unexpected status for case %s", tc.name)
			if tc.wantIDInBody {
				var body map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				assert.Contains(t, string(body["data"]), targetID)
			}
		})
	}
}
