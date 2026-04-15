package rbaccheck

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
)

// newContractRBACHandler builds a full-path mux matching the contract-declared
// routes (/api/v1/access/roles/...) so the contract test covers the complete
// routing chain, not just the relative handler paths.
func newContractRBACHandler() http.Handler {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "r1", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
		},
	})
	_ = roleRepo.AssignToUser(context.Background(), "user-1", "r1")
	svc := NewService(roleRepo, slog.Default())

	inner := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(inner)

	outer := http.NewServeMux()
	outer.Handle("/api/v1/access/roles/", http.StripPrefix("/api/v1/access/roles", inner))
	return outer
}

func TestHttpAuthRoleListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.role.list.v1")
	h := newContractRBACHandler()

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{userID}", "user-1", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("user-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpAuthRoleCheckV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.role.check.v1")
	h := newContractRBACHandler()

	rec := httptest.NewRecorder()
	path := strings.Replace(c.HTTP.Path, "{userID}", "user-1", 1)
	path = strings.Replace(path, "{roleName}", "admin", 1)
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	req = req.WithContext(auth.TestContext("user-1", nil))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
}
