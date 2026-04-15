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
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
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

	err := svc.Logout(context.Background(), sessID)
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Logout must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"session_id":"s"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

// --- Outbox error propagation test (S3-F1) ---

func TestService_Logout_OutboxWriteError(t *testing.T) {
	sessionRepo := mem.NewSessionRepository()
	seedContractSession(sessionRepo)
	failWriter := &recordingWriter{err: errors.New("outbox unavailable")}
	svc := NewService(sessionRepo, eventbus.New(), slog.Default(),
		WithOutboxWriter(failWriter), WithTxManager(noopTxRunner{}))

	err := svc.Logout(context.Background(), "sess-1")
	require.Error(t, err, "Logout must propagate outbox.Write error to preserve L2 atomicity")
	assert.Contains(t, err.Error(), "outbox")
}
