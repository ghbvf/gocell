package sessionprojection_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/sessionprojection"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// tenantUUID1 and tenantUUID2 are canonical UUIDs used across subtests.
const (
	tenantUUID1 = "aaaaaaaa-0000-0000-0000-000000000001"
	tenantUUID2 = "bbbbbbbb-0000-0000-0000-000000000002"
)

// fakeEntry implements cellvocab.ProjectionEvent for test purposes.
// RestoreContext injects the tenant stored in the entry into the context,
// mirroring the real Coordinator's behavior: it calls entry.RestoreContext(ctx)
// before invoking Apply so that tenant.FromContext(ctx) is authoritative.
type fakeEntry struct {
	eventID   string
	payload   []byte
	tenantStr string // set to inject a tenant via RestoreContext
}

func (f fakeEntry) EventID() string       { return f.eventID }
func (f fakeEntry) Stream() string        { return "event.session.created.v1" }
func (f fakeEntry) Payload() []byte       { return f.payload }
func (f fakeEntry) OccurredAt() time.Time { return time.Time{} }

// RestoreContext stamps the tenant from the entry envelope into the context,
// exactly as the framework Coordinator does. Tests should pass context.Background()
// to HandleSessionCreated and rely on RestoreContext for tenant injection.
func (f fakeEntry) RestoreContext(ctx context.Context) context.Context {
	if f.tenantStr != "" {
		return ctxkeys.WithTenantID(ctx, f.tenantStr)
	}
	return ctx
}

func mustPayload(t *testing.T, sessionID, userID string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"sessionId": sessionID, "userId": userID})
	require.NoError(t, err)
	return b
}

// applyEntry simulates how the Coordinator drives Apply: RestoreContext first,
// then HandleSessionCreated with the enriched context. This is the pattern all
// test helpers should follow to stay consistent with production behavior.
func applyEntry(ctx context.Context, svc *sessionprojection.Service, entry fakeEntry) error {
	enriched := entry.RestoreContext(ctx)
	return svc.HandleSessionCreated(enriched, entry)
}

func newSvc(t *testing.T) *sessionprojection.Service {
	t.Helper()
	svc, err := sessionprojection.NewService()
	require.NoError(t, err)
	return svc
}

// isPermanent checks whether err wraps an outbox.PermanentError.
func isPermanent(err error) bool {
	var pe *outbox.PermanentError
	return errors.As(err, &pe)
}

// TestHandleSessionCreated_Apply verifies that a well-formed session.created entry
// is stored in the correct tenant bucket.
func TestHandleSessionCreated_Apply(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)
	entry := fakeEntry{eventID: "evt-1", payload: mustPayload(t, "sess-A", "user-1"), tenantStr: tenantUUID1}

	err := applyEntry(context.Background(), svc, entry)
	require.NoError(t, err)

	tid, err := tenant.ParseTenantID(tenantUUID1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), svc.Query(context.Background(), tid))
}

// TestHandleSessionCreated_Idempotent verifies that applying the same session ID
// twice does not double-count (set semantics, idempotent on rebuild replay).
func TestHandleSessionCreated_Idempotent(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)
	entry := fakeEntry{eventID: "evt-dup", payload: mustPayload(t, "sess-B", "user-1"), tenantStr: tenantUUID1}

	require.NoError(t, applyEntry(context.Background(), svc, entry))
	require.NoError(t, applyEntry(context.Background(), svc, entry))

	tid, _ := tenant.ParseTenantID(tenantUUID1)
	assert.Equal(t, int64(1), svc.Query(context.Background(), tid))
}

// TestHandleSessionCreated_TenantIsolation verifies that sessions for two different
// tenants do not bleed into each other's counts.
func TestHandleSessionCreated_TenantIsolation(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	require.NoError(t, applyEntry(context.Background(), svc,
		fakeEntry{eventID: "e1", payload: mustPayload(t, "s1", "u1"), tenantStr: tenantUUID1}))
	require.NoError(t, applyEntry(context.Background(), svc,
		fakeEntry{eventID: "e2", payload: mustPayload(t, "s2", "u1"), tenantStr: tenantUUID1}))
	require.NoError(t, applyEntry(context.Background(), svc,
		fakeEntry{eventID: "e3", payload: mustPayload(t, "s3", "u2"), tenantStr: tenantUUID2}))

	tid1, _ := tenant.ParseTenantID(tenantUUID1)
	tid2, _ := tenant.ParseTenantID(tenantUUID2)
	assert.Equal(t, int64(2), svc.Query(context.Background(), tid1), "tenant1 should have 2 sessions")
	assert.Equal(t, int64(1), svc.Query(context.Background(), tid2), "tenant2 should have 1 session")
}

// TestHandleSessionCreated_MissingTenant verifies that a context without a tenant
// (violating the outbox envelope contract) results in a permanent error (fail-closed).
func TestHandleSessionCreated_MissingTenant(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)
	// tenantStr intentionally empty so RestoreContext does not inject a tenant.
	entry := fakeEntry{eventID: "evt-no-tenant", payload: mustPayload(t, "sess-C", "user-1")}

	err := applyEntry(context.Background(), svc, entry)
	require.Error(t, err)
	assert.True(t, isPermanent(err), "missing tenant must be a permanent error")
}

// TestHandleSessionCreated_EmptySessionID verifies that an empty sessionId in the
// payload is a permanent error (producer-side schema violation).
func TestHandleSessionCreated_EmptySessionID(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)
	entry := fakeEntry{eventID: "evt-empty-sid", payload: mustPayload(t, "", "user-1"), tenantStr: tenantUUID1}

	err := applyEntry(context.Background(), svc, entry)
	require.Error(t, err)
	assert.True(t, isPermanent(err), "empty sessionId must be a permanent error")
}

// TestHandleSessionCreated_BadJSON verifies that an undecodable payload is a
// permanent error (permanently malformed producer payload).
func TestHandleSessionCreated_BadJSON(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)
	entry := fakeEntry{eventID: "evt-bad-json", payload: []byte("not-json"), tenantStr: tenantUUID1}

	err := applyEntry(context.Background(), svc, entry)
	require.Error(t, err)
	assert.True(t, isPermanent(err), "undecodable payload must be a permanent error")
}

// TestResetSessionRegistry verifies that reset clears all tenant buckets so a
// rebuild can reconstruct the model from scratch.
func TestResetSessionRegistry(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	require.NoError(t, applyEntry(context.Background(), svc,
		fakeEntry{eventID: "e1", payload: mustPayload(t, "s1", "u1"), tenantStr: tenantUUID1}))
	require.NoError(t, applyEntry(context.Background(), svc,
		fakeEntry{eventID: "e2", payload: mustPayload(t, "s2", "u1"), tenantStr: tenantUUID1}))

	require.NoError(t, svc.ResetSessionRegistry(context.Background()))

	tid, _ := tenant.ParseTenantID(tenantUUID1)
	assert.Equal(t, int64(0), svc.Query(context.Background(), tid), "after reset all session counts must be zero")
}

// TestQuery_UnknownTenant verifies that querying a tenant with no sessions
// returns 0 without error (empty-result case).
func TestQuery_UnknownTenant(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)
	tid, _ := tenant.ParseTenantID(tenantUUID1)
	assert.Equal(t, int64(0), svc.Query(context.Background(), tid))
}

// TestHandleSessionCreated_MultipleSessionsSameTenant verifies accumulation within one tenant.
func TestHandleSessionCreated_MultipleSessionsSameTenant(t *testing.T) {
	t.Parallel()
	svc := newSvc(t)

	for i, sid := range []string{"s-1", "s-2", "s-3"} {
		entry := fakeEntry{
			eventID:   "evt-" + sid,
			payload:   mustPayload(t, sid, "user-x"),
			tenantStr: tenantUUID1,
		}
		require.NoError(t, applyEntry(context.Background(), svc, entry), "apply #%d", i)
	}

	tid, _ := tenant.ParseTenantID(tenantUUID1)
	assert.Equal(t, int64(3), svc.Query(context.Background(), tid))
}
