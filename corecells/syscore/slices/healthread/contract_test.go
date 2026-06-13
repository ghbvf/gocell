package healthread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// unavailableAuthorizer simulates the PDP policy store being down: Authorize
// returns a KindUnavailable errcode, which RequirePermission passes through
// verbatim so httputil maps it to 503 (the second 503 branch the contract
// declares, distinct from the no-HealthView fail-closed 503).
func unavailableAuthorizer() *mockAuthorizer {
	return &mockAuthorizer{err: errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "policy store unavailable")}
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
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
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
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
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
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractCellsServe_PDPUnavailable503: an authenticated admin whose PDP
// store is down (Authorize returns KindUnavailable) gets the SECOND declared 503
// branch — distinct from the no-HealthView fail-closed 503 above. The body must
// satisfy the shared error schema and carry the unavailable code (proving the
// request stopped at the PDP gate, not the handler).
func TestContractCellsServe_PDPUnavailable503(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1",
		Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, unavailableAuthorizer())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v; body=%s", err, rec.Body.String())
	}
	if env.Error.Code != string(errcode.ErrServiceUnavailable) {
		t.Fatalf("error code = %q, want %s (PDP store unavailable path)", env.Error.Code, errcode.ErrServiceUnavailable)
	}
}
