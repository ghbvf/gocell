package configwrite

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/stretchr/testify/require"
)

func newContractService() (*Service, *mem.ConfigRepository, *recordingWriter) {
	repo := mem.NewConfigRepository()
	writer := &recordingWriter{}
	svc := NewService(repo, stubPublisher{}, slog.Default(),
		WithOutboxWriter(writer), WithTxManager(&noopTxRunner{}))
	return svc, repo, writer
}

func TestEventConfigChangedV1Publish_Create(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService()

	_, err := svc.Create(context.Background(), CreateInput{Key: "app.name", Value: "myapp"})
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Create must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	c.MustRejectPayload(t, []byte(`{"action":"created","key":"app.name"}`))
	c.MustRejectHeaders(t, []byte(`{}`))
}

func TestEventConfigChangedV1Publish_Update(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService()

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v1"})
	require.NoError(t, err)
	writer.entries = nil // reset

	_, err = svc.Update(context.Background(), UpdateInput{Key: "k", Value: "v2"})
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Update must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}

func TestEventConfigChangedV1Publish_Delete(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.config.changed.v1")
	svc, _, writer := newContractService()

	_, err := svc.Create(context.Background(), CreateInput{Key: "k", Value: "v"})
	require.NoError(t, err)
	writer.entries = nil // reset

	err = svc.Delete(context.Background(), "k")
	require.NoError(t, err)

	require.Len(t, writer.entries, 1, "Delete must emit one outbox entry")
	entry := writer.entries[0]
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
}
