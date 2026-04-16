package rbacassign

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
)

func newContractHandler() http.Handler {
	roleRepo := mem.NewRoleRepository()
	roleRepo.SeedRole(&domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	})
	_ = roleRepo.AssignToUser(context.Background(), "usr-seed", "admin")

	svc := NewService(roleRepo, slog.Default())
	inner := celltest.NewTestMux()
	NewHandler(svc).RegisterRoutes(inner)

	outer := http.NewServeMux()
	outer.Handle("/internal/v1/access/roles/", http.StripPrefix("/internal/v1/access/roles", inner))
	return outer
}

func TestHttpAuthRoleAssignV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.role.assign.v1")
	handler := newContractHandler()

	// Validate request schema.
	c.ValidateRequest(t, []byte(`{"userId":"usr-2","roleId":"admin"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-2"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-2","roleId":"admin","extra":"bad"}`))

	// Execute real handler.
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"userId":"usr-2","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("usr-seed", []string{"admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
	require.Equal(t, 200, rec.Code)

	// Reject invalid response shape.
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthRoleRevokeV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.role.revoke.v1")
	handler := newContractHandler()

	// Validate request schema.
	c.ValidateRequest(t, []byte(`{"userId":"usr-seed","roleId":"admin"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-seed"}`))

	// Execute real handler.
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"userId":"usr-seed","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestContext("usr-seed", []string{"admin"}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
	require.Equal(t, 200, rec.Code)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
