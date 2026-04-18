package rbacassign

import (
	"context"
	"encoding/json"
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
	_ = roleRepo.AssignToUser(context.Background(), "usr-other-admin", "admin") // second admin for last-admin guard

	svc := NewService(roleRepo, mem.NewSessionRepository(), slog.Default())
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
	require.Equal(t, http.StatusCreated, rec.Code)

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

// TestContract_EventRoleAssignedV1_Publish_PayloadValid is a minimum-viability smoke test
// that marshals RoleChangedEvent (action=assigned) and validates it against the
// event.role.assigned.v1 payload JSON Schema.
// Full contract coverage is tracked as S8-FOLLOWUP (VERIFY-01 waiver expiry 2026-07-01).
func TestContract_EventRoleAssignedV1_Publish_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.role.assigned.v1")

	evt := RoleChangedEvent{UserID: "u1", RoleID: "admin", Action: ActionAssigned}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)

	// Positive: well-formed payload must pass schema.
	c.ValidatePayload(t, payload)

	// Negative: missing userId must FAIL schema (required field absent).
	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"assigned"}`))
}

// TestContract_EventRoleRevokedV1_Publish_PayloadValid is a minimum-viability smoke test
// that marshals RoleChangedEvent (action=revoked) and validates it against the
// event.role.revoked.v1 payload JSON Schema.
// Full contract coverage is tracked as S8-FOLLOWUP (VERIFY-01 waiver expiry 2026-07-01).
func TestContract_EventRoleRevokedV1_Publish_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.role.revoked.v1")

	evt := RoleChangedEvent{UserID: "u1", RoleID: "admin", Action: ActionRevoked}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)

	// Positive: well-formed payload must pass schema.
	c.ValidatePayload(t, payload)

	// Negative: missing userId must FAIL schema (required field absent).
	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"revoked"}`))
}
