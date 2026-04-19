package sessionlogout

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func seedContractSession(repo *mem.SessionRepository) string {
	sess, _ := domain.NewSession("usr-1", "at-1", "rt-1", time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = repo.Create(context.Background(), sess)
	return sess.ID
}

// --- HTTP contract test (S1-F1) ---

func TestHttpAuthSessionDeleteV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.session.delete.v1")

	sessionRepo := mem.NewSessionRepository()
	sessID := seedContractSession(sessionRepo)
	svc := NewService(sessionRepo, eventbus.New(), slog.Default(),
		WithOutboxWriter(&recordingWriter{}), WithTxManager(noopTxRunner{}))

	mux := http.NewServeMux()
	mux.Handle("DELETE /api/v1/access/sessions/{id}", http.HandlerFunc(NewHandler(svc).HandleLogout))

	c.ValidateRequest(t, []byte(`{}`))
	c.MustRejectRequest(t, []byte(`{"unexpected":true}`))

	path := strings.Replace(c.HTTP.Path, "{id}", sessID, 1)
	rec := httptest.NewRecorder()
	// Simulate the auth middleware having populated the caller's subject in ctx.
	req := httptest.NewRequest(c.HTTP.Method, path, nil).
		WithContext(auth.TestContext("usr-1", nil))
	mux.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
}

// --- Event contract test ---

func TestEventSessionRevokedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.revoked.v1")

	sessionRepo := mem.NewSessionRepository()
	writer := &recordingWriter{}
	svc := NewService(sessionRepo, eventbus.New(), slog.Default(),
		WithOutboxWriter(writer), WithTxManager(noopTxRunner{}))

	sessID := seedContractSession(sessionRepo)

	err := svc.Logout(context.Background(), sessID, "usr-1")
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Logout must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"session_id":"s"}`))
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
// Paired with the publish-side test in cells/access-core/slices/rbacassign/
// contract_test.go — together they cover both halves of the contract, which
// is why the slice.yaml waiver for VERIFY-01 no longer applies.
func TestContract_EventRoleAssignedV1_Subscribe_PayloadValid(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.role.assigned.v1")

	repo := mem.NewSessionRepository()
	consumer := NewConsumer(repo, slog.Default())
	handler := outbox.WrapLegacyHandler(consumer.HandleRoleChanged)

	payload := []byte(`{"userId":"usr-123","roleId":"admin","action":"assigned"}`)
	c.ValidatePayload(t, payload)
	result := handler(context.Background(), outbox.Entry{
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
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.role.revoked.v1")

	repo := mem.NewSessionRepository()
	consumer := NewConsumer(repo, slog.Default())
	handler := outbox.WrapLegacyHandler(consumer.HandleRoleChanged)

	payload := []byte(`{"userId":"usr-123","roleId":"admin","action":"revoked"}`)
	c.ValidatePayload(t, payload)
	result := handler(context.Background(), outbox.Entry{
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
	sessionRepo := mem.NewSessionRepository()
	seedContractSession(sessionRepo)
	failWriter := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(sessionRepo, eventbus.New(), slog.Default(),
		WithOutboxWriter(failWriter), WithTxManager(noopTxRunner{}))

	err := svc.Logout(context.Background(), "sess-1", "usr-1")
	require.Error(t, err, "Logout must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}
