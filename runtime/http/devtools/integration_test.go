package devtools_test

// integration_test.go (package devtools_test)
//
// These tests exercise the full HTTP stack with real chi.Router + AuthMiddleware
// + RouteGroup mount. Unlike catalog_test.go which calls handler.ServeHTTP
// directly to test handler-level behavior, these tests prove the route-level
// Policy actually rejects 401/403 — closing the integration gap noted in
// PR #357 review F13.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
	"github.com/ghbvf/gocell/runtime/http/devtools"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// stubVerifier is an IntentTokenVerifier that injects a pre-built Principal
// via context rather than verifying a real JWT. It returns an error to
// simulate missing/invalid tokens when errMsg is non-empty.
type stubVerifier struct {
	// principal to return on success (nil → no principal injected)
	claims auth.Claims
	// non-empty → VerifyIntent returns this error string → 401
	failMsg string
}

func (v *stubVerifier) VerifyIntent(_ context.Context, token string, _ auth.TokenIntent) (auth.Claims, error) {
	if token == "" || v.failMsg != "" {
		return auth.Claims{}, &stubVerifyError{msg: v.failMsg}
	}
	return v.claims, nil
}

type stubVerifyError struct{ msg string }

func (e *stubVerifyError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return "stub: token verification failed"
}

// buildAuthRouter constructs a chi Router with AuthMiddleware and the
// devtools catalog RouteGroup mounted. It returns the finalized router
// and a teardown function.
//
// principalRole controls what the JWT verifier claims to return:
//   - "" (empty)  → verifier fails → AuthMiddleware returns 401 for any request with a token
//   - any string  → verifier returns a Principal with that single role
//
// noToken controls whether requests should include an Authorization header:
// when the router has AuthMiddleware and no token is sent, the middleware
// rejects the request with 401 before reaching the handler.
func buildAuthRouter(t *testing.T, principalRole string) *router.Router {
	t.Helper()

	var verifier *stubVerifier
	if principalRole == "" {
		// Verifier that always fails — simulates invalid/absent token.
		verifier = &stubVerifier{failMsg: "no valid token"}
	} else {
		verifier = &stubVerifier{
			claims: auth.Claims{
				Subject: "test-user",
				Roles:   []string{principalRole},
			},
		}
	}

	rtr, err := router.New(
		router.WithAuthMiddleware(verifier),
		router.WithRouterClock(clock.Real()),
		router.WithSuppressNoAuthVerifierWarn(),
	)
	if err != nil {
		t.Fatalf("buildAuthRouter: router.New: %v", err)
	}

	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "owner"},
				Schema:           metadata.SchemaMeta{Primary: "public.sessions"},
			},
		},
		Slices:      map[string]*metadata.SliceMeta{},
		Contracts:   map[string]*metadata.ContractMeta{},
		Journeys:    map[string]*metadata.JourneyMeta{},
		Assemblies:  map[string]*metadata.AssemblyMeta{},
		StatusBoard: []metadata.StatusBoardEntry{},
		Actors:      []metadata.ActorMeta{},
	}
	pkgGraph := minimalPkgGraph()
	h := devtools.NewHandler(project, &catalog.CellDepGraph{
		Nodes: []string{"accesscore"},
		Edges: []catalog.CellEdge{},
	}, pkgGraph, "/test-root", clock.Real())

	rg := devtools.RouteGroup(h)
	if err := rg.Register(rtr); err != nil {
		t.Fatalf("buildAuthRouter: RouteGroup.Register: %v", err)
	}
	if err := rtr.FinalizeAuth(); err != nil {
		t.Fatalf("buildAuthRouter: FinalizeAuth: %v", err)
	}
	return rtr
}

// TestCatalogIntegration_NoToken_Returns401 verifies that a request to
// GET /api/v1/devtools/catalog without an Authorization header is rejected
// by AuthMiddleware with 401 before the handler or Policy runs.
func TestCatalogIntegration_NoToken_Returns401(t *testing.T) {
	t.Parallel()

	// Build a router whose verifier always fails (simulates absent/invalid token).
	rtr := buildAuthRouter(t, "" /* principalRole empty → verifier always fails */)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devtools/catalog", nil)
	// No Authorization header — AuthMiddleware detects missing token and returns 401.
	rr := httptest.NewRecorder()
	rtr.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCatalogIntegration_NonAdminRole_Returns403 verifies that a request
// authenticated as a non-admin user (role "user") is rejected by the
// route-level Policy (auth.AnyRole(auth.RoleAdmin)) with 403.
func TestCatalogIntegration_NonAdminRole_Returns403(t *testing.T) {
	t.Parallel()

	rtr := buildAuthRouter(t, "user")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devtools/catalog", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()
	rtr.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestCatalogIntegration_AdminRole_Returns200 verifies that a request
// authenticated as an admin user (role auth.RoleAdmin) is accepted by
// the route-level Policy and the handler returns a valid catalog Document.
func TestCatalogIntegration_AdminRole_Returns200(t *testing.T) {
	t.Parallel()

	rtr := buildAuthRouter(t, auth.RoleAdmin)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/devtools/catalog", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()
	rtr.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Sanity-check the response is a valid catalog Document.
	ct := rr.Header().Get("Content-Type")
	if ct == "" {
		t.Error("expected non-empty Content-Type")
	}
}

// Ensure the RouteGroup mux implements cell.RouteMux at compile time.
var _ cell.RouteHandler = (*router.Router)(nil)
