package rbacassign

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

// TestContract_EventRoleAssignedV1_Publish_PayloadValid drives the rbacassign
// Service through a real Assign call and validates the captured outbox entry
// against the event.role.assigned.v1 payload schema. This replaces the prior
// smoke-only test (B2-T-02 waiver expiry 2026-07-01 — closed by S4c-T1).
func TestContract_EventRoleAssignedV1_Publish_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.role.assigned.v1")

	ow := &testutil.RecordingWriter{}
	tx := &stubTxRunner{}
	svc, _, _ := newDurableTestService(t, ow, tx)

	require.NoError(t, svc.Assign(context.Background(), "alice", "admin"))

	require.Len(t, ow.Entries, 1, "Assign must emit exactly one outbox entry")
	entry := ow.Entries[0]
	assert.Equal(t, dto.TopicRoleAssigned, entry.EventType)
	assert.NotEmpty(t, entry.ID, "emitter must derive a non-empty event_id")

	// Real emit must pass payload schema.
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))

	// Negative path: malformed payload must fail schema.
	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"assigned"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

// TestContract_EventRoleRevokedV1_Publish_PayloadValid drives the rbacassign
// Service through a real Revoke call and validates the captured outbox entry
// against the event.role.revoked.v1 payload schema. This replaces the prior
// smoke-only test (B2-T-02 waiver expiry 2026-07-01 — closed by S4c-T1).
func TestContract_EventRoleRevokedV1_Publish_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.role.revoked.v1")

	ow := &testutil.RecordingWriter{}
	tx := &stubTxRunner{}
	svc, store, _ := newDurableTestService(t, ow, tx)
	// Two effective admins so the last-admin guard does not block Revoke.
	assignActiveAdmin(t, store, "alice")
	assignActiveAdmin(t, store, "bob")

	require.NoError(t, svc.Revoke(context.Background(), "alice", "admin"))

	require.Len(t, ow.Entries, 1, "Revoke must emit exactly one outbox entry")
	entry := ow.Entries[0]
	assert.Equal(t, dto.TopicRoleRevoked, entry.EventType)
	assert.NotEmpty(t, entry.ID, "emitter must derive a non-empty event_id")

	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))

	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"revoked"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
