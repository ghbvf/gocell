package registryread

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/registry"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const contractID = "http.registry.contract.list.v1"

var testEpoch = mustTime("2026-06-18T00:00:00Z")

// mockAuthorizer is a test-only auth.Authorizer returning a fixed Decision.
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

// newMuxOver mounts the list handler over the given registrar under the
// production-mirroring prefix /api/v1/registry (auth.Mount strips it off
// Contract.Path exactly as the cellgen route group does). RegisterRoutes installs
// the registry:read RequirePermission policy.
func newMuxOver(t *testing.T, registrar *registry.ContractRegistrar) http.Handler {
	t.Helper()
	svc, err := NewService(registrar)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := NewHandler(svc)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/registry", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

func emptyRegistrar() *registry.ContractRegistrar {
	return registry.NewContractRegistrar(clockmock.New(testEpoch))
}

func adminCtx(authorizer auth.Authorizer) context.Context {
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1", Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	return auth.WithAuthorizer(ctx, authorizer)
}

func getList(t *testing.T, mux http.Handler, ctx context.Context, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	path := "/api/v1/registry/contracts"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	mux.ServeHTTP(rec, req)
	return rec
}

// TestContractListServe_QuerySchema covers every declared query parameter with a
// positive and ≥1 negative case (CONTRACT-PATH-QUERY-COVERAGE-01).
func TestContractListServe_QuerySchema(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	// limit: integer 1..500
	c.ValidateQueryParam(t, "limit", "1")
	c.ValidateQueryParam(t, "limit", "500")
	c.MustRejectQueryParam(t, "limit", "0")   // below minimum
	c.MustRejectQueryParam(t, "limit", "501") // above maximum
	c.MustRejectQueryParam(t, "limit", "abc") // wrong type
	// cursor: string maxLength 4096
	c.ValidateQueryParam(t, "cursor", "http.example.foo.v1")
	c.MustRejectQueryParam(t, "cursor", strings.Repeat("x", 4097)) // over maxLength
}

// TestContractListServe_OK: an authenticated admin (allow PDP) gets a 200 whose
// body satisfies the paginated response schema (empty page is valid).
func TestContractListServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, emptyRegistrar()), adminCtx(allowAuthorizer()), "limit=10")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

// TestContractListServe_Unauthenticated: no principal ⇒ RequirePermission 401.
func TestContractListServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, emptyRegistrar()), context.Background(), "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractListServe_Forbidden: authenticated but the PDP denies registry:read ⇒ 403.
func TestContractListServe_Forbidden(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := getList(t, newMuxOver(t, emptyRegistrar()), adminCtx(denyAuthorizer("no registry:read")), "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

// TestContractListServe_BadRequest: an out-of-range limit (below the minimum 1 /
// above the maximum 500) ⇒ 400 from the handler's ParsePageParams, with a shared
// error envelope. Complements the schema-level MustRejectQueryParam cases with the
// real HTTP path the contract declares.
func TestContractListServe_BadRequest(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	for _, q := range []string{"limit=0", "limit=501"} {
		rec := getList(t, newMuxOver(t, emptyRegistrar()), adminCtx(allowAuthorizer()), q)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q: status = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
		c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
	}
}
