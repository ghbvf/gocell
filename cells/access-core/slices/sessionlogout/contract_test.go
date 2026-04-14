package sessionlogout

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/require"
)

// --- contract test doubles ---

type recordingWriter struct {
	entries []outbox.Entry
}

func (w *recordingWriter) Write(_ context.Context, e outbox.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = noopTxRunner{}

func TestEventSessionRevokedV1Publish(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.session.revoked.v1")

	sessionRepo := mem.NewSessionRepository()
	writer := &recordingWriter{}
	svc := NewService(sessionRepo, eventbus.New(), slog.Default(),
		WithOutboxWriter(writer), WithTxManager(noopTxRunner{}))

	// Seed a session to revoke.
	sess, _ := domain.NewSession("usr-1", "at-1", "rt-1", time.Now().Add(time.Hour))
	sess.ID = "sess-1"
	_ = sessionRepo.Create(context.Background(), sess)

	err := svc.Logout(context.Background(), "sess-1")
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Logout must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"session_id":"s"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}
