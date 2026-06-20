package systemread

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cell/celltest"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/sysinfo"
	"github.com/ghbvf/gocell/tests/contracttest"
)

const contractID = "http.admin.system.v1"

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

func unavailableAuthorizer() *mockAuthorizer {
	return &mockAuthorizer{err: errcode.New(errcode.KindUnavailable, errcode.ErrServiceUnavailable, "policy store unavailable")}
}

var testSystemResolver = auth.NewStaticMethodPolicyResolver(map[string]string{
	"http.admin.system.v1": authz.PermSystemRead().String(),
})

func newMux(t *testing.T) http.Handler {
	t.Helper()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	h := NewHandler(svc, testSystemResolver)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/admin", func(sub cell.RouteMux) {
		if err := h.RegisterRoutes(sub); err != nil {
			t.Fatalf("RegisterRoutes: %v", err)
		}
	})
	return mux
}

func TestContractSystemServe_OK(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{
		Kind: auth.PrincipalUser, Subject: "admin-1",
		Roles: []string{auth.RoleAdmin}, AuthMethod: "test",
	})
	ctx = auth.WithAuthorizer(ctx, allowAuthorizer())
	ctx = sysinfo.WithSystemView(ctx, fakeSystemView{sampleSystemReport()})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil).WithContext(ctx)
	newMux(t).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestContractSystemServe_Unauthenticated(t *testing.T) {
	c := contracttest.LoadByID(t, contracttest.ContractsRoot(t), contractID)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, nil)
	newMux(t).ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
	c.ValidateErrorResponse(t, rec.Code, rec.Body.Bytes())
}

func TestContractSystemServe_Forbidden(t *testing.T) {
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

func TestContractSystemServe_FailClosed503(t *testing.T) {
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

func TestContractSystemServe_PDPUnavailable503(t *testing.T) {
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
		t.Fatalf("error code = %q, want %s", env.Error.Code, errcode.ErrServiceUnavailable)
	}
}
