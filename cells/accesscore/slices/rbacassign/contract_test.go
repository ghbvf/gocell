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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/tests/contracttest"
)

// testTenantID is the canonical test tenant UUID used in rbacassign tests.
var testTenantID = func() tenant.TenantID {
	t, err := tenant.ParseTenantID("00000000-0000-0000-0000-000000000001")
	if err != nil {
		panic("rbacassign_test: invalid testTenantID: " + err.Error())
	}
	return t
}()

const testTenantIDStr = "00000000-0000-0000-0000-000000000001"

// testAuthServiceCtx wraps auth.TestServiceContext with the canonical test tenant.
func testAuthServiceCtx(callerCell string) context.Context {
	return ctxkeys.WithTenantID(auth.TestServiceContext(callerCell), testTenantIDStr)
}

// testAuthUserCtx wraps auth.TestContext with the canonical test tenant.
func testAuthUserCtx(userID string, roles []string) context.Context {
	return ctxkeys.WithTenantID(auth.TestContext(userID, roles), testTenantIDStr)
}

// tenantCtx returns context.Background() with the canonical test tenant.
func tenantCtx() context.Context {
	return ctxkeys.WithTenantID(context.Background(), testTenantIDStr)
}

func newContractHandler(t *testing.T) http.Handler {
	t.Helper()
	store := mem.NewStore(clock.Real())
	store.RoleRepository().SeedRole(testTenantID, &domain.Role{
		ID: "admin", Name: "admin",
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	})
	// Seed two effective admins so the contract revoke test passes the
	// effective-admin guard.
	for _, uid := range []string{"usr-seed", "usr-other-admin"} {
		cu, cuErr := domain.NewUser(uid, uid+"@test.local", "$2a$12$hash", time.Now())
		require.NoError(t, cuErr)
		cu.ID = uid
		require.NoError(t, store.UserRepository().Create(context.Background(), testTenantID, cu))
		_, err := store.RoleRepository().AssignToUser(context.Background(), testTenantID, uid, "admin")
		require.NoError(t, err)
	}
	// Option B: Assign/Revoke derive tenant from the target user — seed the
	// roster (usr-2, alice, ...) the contract tests operate on.
	seedTestUserRoster(t, store)

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
	c.ValidateRequest(t, []byte(`{"userId":"usr-2","roleId":"admin","tenantId":"00000000-0000-0000-0000-000000000001"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-2","tenantId":"00000000-0000-0000-0000-000000000001"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-2","roleId":"admin","tenantId":"00000000-0000-0000-0000-000000000001","extra":"bad"}`))

	// Execute real handler.
	// Spec: use TestServiceContext("accesscore") — caller-cell identity replaces role-based auth.
	const assignBody = `{"userId":"usr-2","roleId":"admin","tenantId":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(assignBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(testAuthServiceCtx("accesscore"))
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
	c.ValidateRequest(t, []byte(`{"userId":"usr-seed","roleId":"admin","tenantId":"00000000-0000-0000-0000-000000000001"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-seed","tenantId":"00000000-0000-0000-0000-000000000001"}`))
	c.MustRejectRequest(t, []byte(`{"userId":"usr-seed","roleId":"admin","tenantId":"00000000-0000-0000-0000-000000000001","extra":"bad"}`))

	// Execute real handler.
	// Spec: use TestServiceContext("accesscore") — caller-cell identity replaces role-based auth.
	const revokeBody = `{"userId":"usr-seed","roleId":"admin","tenantId":"00000000-0000-0000-0000-000000000001"}`
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(revokeBody))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(testAuthServiceCtx("accesscore"))
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

	require.NoError(t, svc.Assign(tenantCtx(), testTenantID, "alice", "admin"))

	require.Len(t, ow.Entries, 1, "Assign must emit exactly one outbox entry")
	entry := ow.Entries[0]
	assert.Equal(t, dto.TopicRoleAssigned, entry.EventType())
	assert.NotEmpty(t, entry.ID(), "emitter must derive a non-empty eventId")
	assert.True(t, strings.HasPrefix(entry.ID(), outbox.EntryIDPrefix),
		"entry.ID() %q must have %q prefix (eventId schema format)", entry.ID(), outbox.EntryIDPrefix)

	// Real emit must pass payload schema.
	c.ValidatePayload(t, entry.Payload())
	headerBytes, err := json.Marshal(map[string]string{"eventId": entry.ID()})
	require.NoError(t, err)
	c.ValidateHeaders(t, headerBytes)

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

	require.NoError(t, svc.Revoke(tenantCtx(), testTenantID, "alice", "admin"))

	require.Len(t, ow.Entries, 1, "Revoke must emit exactly one outbox entry")
	entry := ow.Entries[0]
	assert.Equal(t, dto.TopicRoleRevoked, entry.EventType())
	assert.NotEmpty(t, entry.ID(), "emitter must derive a non-empty eventId")
	assert.True(t, strings.HasPrefix(entry.ID(), outbox.EntryIDPrefix),
		"entry.ID() %q must have %q prefix (eventId schema format)", entry.ID(), outbox.EntryIDPrefix)

	c.ValidatePayload(t, entry.Payload())
	headerBytes, err := json.Marshal(map[string]string{"eventId": entry.ID()})
	require.NoError(t, err)
	c.ValidateHeaders(t, headerBytes)

	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"revoked"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
