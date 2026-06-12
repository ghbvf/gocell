package healthread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/syshealth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const contractID = "http.admin.health.cells.v1"

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision
// (mock placement per go-standards.md §Naming — same-package test file).
type mockAuthorizer struct {
	decision authz.Decision
	err      error
}

func (m *mockAuthorizer) Authorize(context.Context, string, string, string) (authz.Decision, error) {
	return m.decision, m.err
}

func allowAuthorizer() *mockAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("allowAuthorizer: " + err.Error())
	}
	return &mockAuthorizer{decision: dec}
}

func denyAuthorizer(reason string) *mockAuthorizer {
	return &mockAuthorizer{decision: authz.Deny(reason)}
}

// newMux mounts the handler under the production-mirroring prefix (/api/v1/admin)
// so auth.Mount strips it off Contract.Path exactly as the cellgen route group
// does (RouteGroup prefix /api/v1 + mux.Route("/admin")). RegisterRoutes installs
// the system:read RequirePermission policy, so the contract test exercises the
// same gate production uses.
func newMux(t *testing.T) http.Handler {
	t.Helper()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/admin", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

// TestContractCellsServe_OK: an authenticated admin (allow PDP) with a wired
// HealthView gets a 200 whose body satisfies the contract response schema.
func TestContractCellsServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)

	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1",
		Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, allowAuthorizer())
	ctx = syshealth.WithHealthView(ctx, fakeView{sampleReport()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestContractCellsServe_Unauthenticated: no principal ⇒ RequirePermission 401.
func TestContractCellsServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil) // no principal/authorizer
	newMux(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// TestContractCellsServe_Forbidden: authenticated but the PDP denies system:read ⇒ 403.
func TestContractCellsServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "user-1",
		Roles: []string{"viewer"}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, denyAuthorizer("no system:read"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestContractCellsServe_FailClosed503: authorized admin but NO HealthView wired
// ⇒ the service fail-closes to 503 (not a silent empty 200).
func TestContractCellsServe_FailClosed503(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1",
		Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, allowAuthorizer())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}
