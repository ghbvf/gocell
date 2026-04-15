package rbaccheck

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestHttpAuthRoleListV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.role.list.v1")

	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "r1", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
		},
	})
	_ = roleRepo.AssignToUser(context.Background(), "user-1", "r1")
	svc := NewService(roleRepo, slog.Default())
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	// handler registers GET /{userID}, use relative path
	req := httptest.NewRequest(c.HTTP.Method, "/user-1", nil)
	req = req.WithContext(auth.TestContext("user-1", nil))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

func TestHttpAuthRoleCheckV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.role.check.v1")

	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "r1", Name: "admin",
		Permissions: []domain.Permission{
			{Resource: "users", Action: "read"},
		},
	})
	_ = roleRepo.AssignToUser(context.Background(), "user-1", "r1")
	svc := NewService(roleRepo, slog.Default())
	mux := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	// handler registers GET /{userID}/{roleName}, use relative path
	req := httptest.NewRequest(c.HTTP.Method, "/user-1/admin", nil)
	req = req.WithContext(auth.TestContext("user-1", nil))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
}
