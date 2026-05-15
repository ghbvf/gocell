package rbacassign

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func newContractHandler(t *testing.T) http.Handler {
	t.Helper()
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(&domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	})
	// Seed two effective admins so the contract revoke test passes the
	// effective-admin guard.
	for _, uid := range []string{"usr-seed", "usr-other-admin"} {
		cu, cuErr := domain.NewUser(uid, uid+"@test.local", "$2a$12$hash", time.Now())
		require.NoError(t, cuErr)
		cu.ID = uid
		require.NoError(t, store.UserRepository().Create(context.Background(), cu))
		_, err := store.RoleRepository().AssignToUser(context.Background(), uid, "admin")
		require.NoError(t, err)
	}

	svc := mustNewService(t, store.RoleRepository(), store.UserRepository(), testutil.RealSessionRepo(t), slog.Default())
	mux := celltest.NewTestMux()
	h := NewHandler(svc)
	mux.Route("/internal/v1/access/roles", func(s cell.RouteMux) {
		if err := h.RegisterRoutes(s); err != nil {
			panic("newContractHandler: RegisterRoutes: " + err.Error())
		}
	})
	return mux
}

func TestHttpAuthRoleAssignV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.role.assign.v1")
	handler := newContractHandler(t)

	// Validate request schema.
	c.ValidateRequest(t, []byte(`{"userId":"usr-2","roleId":"admin"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-2"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-2","roleId":"admin","extra":"bad"}`))

	// Execute real handler.
	// Spec: use TestServiceContext("accesscore") — caller-cell identity replaces role-based auth.
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"userId":"usr-2","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestServiceContext("accesscore"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
	require.Equal(t, http.StatusCreated, rec.Code)

	// Reject invalid response shape.
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

func TestHttpAuthRoleRevokeV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.role.revoke.v1")
	handler := newContractHandler(t)

	// Validate request schema.
	c.ValidateRequest(t, []byte(`{"userId":"usr-seed","roleId":"admin"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-seed"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-seed","roleId":"admin","extra":"bad"}`))

	// Execute real handler.
	// Spec: use TestServiceContext("accesscore") — caller-cell identity replaces role-based auth.
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(`{"userId":"usr-seed","roleId":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(auth.TestServiceContext("accesscore"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	c.ValidateHTTPResponseRecorder(t, rec)
	require.Equal(t, http.StatusOK, rec.Code)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}

// TestContract_EventRoleAssignedV1_Publish_PayloadValid is a minimum-viability smoke test
// that marshals RoleChangedEvent (action=assigned) and validates it against the
// event.role.assigned.v1 payload JSON Schema.
// Full contract coverage is tracked as S8-FOLLOWUP (VERIFY-01 waiver expiry 2026-07-01).
func TestContract_EventRoleAssignedV1_Publish_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.role.assigned.v1")

	evt := dto.RoleChangedEvent{UserID: "u1", RoleID: "admin", Action: dto.ActionAssigned}
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
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.role.revoked.v1")

	evt := dto.RoleChangedEvent{UserID: "u1", RoleID: "admin", Action: dto.ActionRevoked}
	payload, err := json.Marshal(evt)
	require.NoError(t, err)

	// Positive: well-formed payload must pass schema.
	c.ValidatePayload(t, payload)

	// Negative: missing userId must FAIL schema (required field absent).
	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"revoked"}`))
}
