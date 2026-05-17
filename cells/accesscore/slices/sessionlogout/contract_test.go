package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func newContractRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

// --- contract test doubles ---

type recordingWriter struct {
	entries []outbox.Entry
	err     error
}

func (w *recordingWriter) Write(_ context.Context, e outbox.Entry) error {
	if w.err != nil {
		return w.err
	}
	w.entries = append(w.entries, e)
	return nil
}

type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = noopTxRunner{}

func seedContractSession(store session.Store) string {
	id := testutil.TestID("sess-1")
	_ = store.Create(context.Background(), &session.Session{
		ID:                id,
		SubjectID:         testutil.TestID("usr-1"),
		JTI:               "jti-" + id,
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
	})
	return id
}

// TestHttpAuthSessionDeleteV1_PathParamConstraints asserts that the id path
// param (format: uuid) schema rejects non-UUID strings.
func TestHttpAuthSessionDeleteV1_PathParamConstraints(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.session.delete.v1")
	c.ValidatePathParam(t, "id", "550e8400-e29b-41d4-a716-446655440000")
	c.MustRejectPathParam(t, "id", "not-a-uuid") // violates format: uuid
}

// --- HTTP contract test (S1-F1) ---

func TestHttpAuthSessionDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.session.delete.v1")

	sessionRepo := testutil.RealSessionRepo(t)
	sessID := seedContractSession(sessionRepo)
	svc, err := NewService(sessionRepo, newContractRefreshStore(), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, &recordingWriter{})), WithTxManager(persistence.WrapForCell(noopTxRunner{})))
	require.NoError(t, err)

	mux := celltest.NewTestMux()
	if err := NewHandler(svc).RegisterRoutes(mux); err != nil {
		t.Fatalf("RegisterRoutes: %v", err)
	}

	c.ValidateRequest(t, []byte(`{}`))
	c.MustRejectRequest(t, []byte(`{"unexpected":true}`))

	path := strings.Replace(c.HTTP.Path, "{id}", sessID, 1)
	rec := httptest.NewRecorder()
	// Simulate the auth middleware having populated the caller's subject in ctx.
	req := httptest.NewRequest(c.HTTP.Method, path, nil).
		WithContext(auth.TestContext(testutil.TestID("usr-1"), nil))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Event contract test ---

func TestEventSessionRevokedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.session.revoked.v1")

	sessionRepo := testutil.RealSessionRepo(t)
	writer := &recordingWriter{}
	svc := mustNewService(sessionRepo, newContractRefreshStore(), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, writer)), WithTxManager(persistence.WrapForCell(noopTxRunner{})))

	sessID := seedContractSession(sessionRepo)

	err := svc.Logout(context.Background(), sessID, testutil.TestID("usr-1"))
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Logout must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"sessionId":"s"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

// --- Role event subscribe contract tests ---

// TestContract_EventRoleAssignedV1_Subscribe_PayloadValid exercises the
// consumer's unmarshal + business path against the event.role.assigned.v1
// payload schema. A well-formed payload must return DispositionAck via
// WrapLegacyHandler; schema-invalid payloads (exercised via
// MustRejectPayload) are rejected by the schema validator without ever
// reaching the consumer.
//
// Paired with the publish-side test in cells/accesscore/slices/rbacassign/
// contract_test.go — together they cover both halves of the contract, which
// is why the slice.yaml waiver for VERIFY-01 no longer applies.
func TestContract_EventRoleAssignedV1_Subscribe_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.role.assigned.v1")

	consumer := NewConsumer(slog.Default())

	payload := []byte(`{"userId":"usr-123","roleId":"admin","action":"assigned"}`)
	c.ValidatePayload(t, payload)
	result := consumer.HandleRoleChanged(context.Background(), outbox.Entry{
		ID:        "evt-test-assigned",
		EventType: "event.role.assigned.v1",
		Payload:   payload,
	})
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"valid assigned payload must yield Ack")

	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"assigned"}`))
}

// TestContract_EventRoleRevokedV1_Subscribe_PayloadValid mirrors the assigned
// test for the revoked topic.
func TestContract_EventRoleRevokedV1_Subscribe_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "event.role.revoked.v1")

	consumer := NewConsumer(slog.Default())

	payload := []byte(`{"userId":"usr-123","roleId":"admin","action":"revoked"}`)
	c.ValidatePayload(t, payload)
	result := consumer.HandleRoleChanged(context.Background(), outbox.Entry{
		ID:        "evt-test-revoked",
		EventType: "event.role.revoked.v1",
		Payload:   payload,
	})
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"valid revoked payload must yield Ack")

	c.MustRejectPayload(t, []byte(`{"roleId":"admin","action":"revoked"}`))
}

// --- Outbox error propagation test (S3-F1) ---

func TestService_Logout_OutboxWriteError(t *testing.T) {
	sessionRepo := testutil.RealSessionRepo(t)
	seedContractSession(sessionRepo)
	failWriter := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := mustNewService(sessionRepo, newContractRefreshStore(), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, failWriter)), WithTxManager(persistence.WrapForCell(noopTxRunner{})))

	err := svc.Logout(context.Background(), testutil.TestID("sess-1"), testutil.TestID("usr-1"))
	require.Error(t, err, "Logout must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}
