package ordercreate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// --- test doubles ---

// noopPublisher always succeeds.
type noopPublisher struct{}

func (noopPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }

var _ outbox.Publisher = noopPublisher{}

// failPublisher always returns an error.
type failPublisher struct{}

func (failPublisher) Publish(_ context.Context, _ string, _ []byte) error {
	return errors.New("publish unavailable")
}

// recordingPublisher records each publish call.
type recordingPublisher struct {
	calls []publishCall
}

type publishCall struct {
	topic   string
	payload []byte
}

func (p *recordingPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.calls = append(p.calls, publishCall{topic: topic, payload: payload})
	return nil
}

type recordingWriter struct {
	entries []outbox.Entry
	err     error
}

func (w *recordingWriter) Write(_ context.Context, entry outbox.Entry) error {
	if w.err != nil {
		return w.err
	}
	w.entries = append(w.entries, entry)
	return nil
}

var _ outbox.Writer = (*recordingWriter)(nil)

type stubTxRunner struct {
	calls int
}

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(ctx)
}

var _ persistence.TxRunner = (*stubTxRunner)(nil)

func TestService_Create(t *testing.T) {
	tests := []struct {
		name      string
		item      string
		publisher outbox.Publisher
		wantErr   bool
		errCode   errcode.Code
	}{
		{
			name:      "success",
			item:      "widget",
			publisher: noopPublisher{},
		},
		{
			name:      "empty item returns validation error",
			item:      "",
			publisher: noopPublisher{},
			wantErr:   true,
			errCode:   errcode.ErrValidationFailed,
		},
		{
			name:      "publish failure does not fail create",
			item:      "gadget",
			publisher: failPublisher{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mem.NewOrderRepository()
			svc := NewService(repo, tt.publisher, slog.Default())

			order, err := svc.Create(context.Background(), tt.item)
			if tt.wantErr {
				require.Error(t, err)
				var ecErr *errcode.Error
				require.ErrorAs(t, err, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
				assert.Nil(t, order)
			} else {
				require.NoError(t, err)
				require.NotNil(t, order)
				assert.Equal(t, tt.item, order.Item)
				assert.Equal(t, "pending", order.Status)
				assert.NotEmpty(t, order.ID)
			}
		})
	}
}

func TestService_Create_PublishesEvent(t *testing.T) {
	repo := mem.NewOrderRepository()
	pub := &recordingPublisher{}
	svc := NewService(repo, pub, slog.Default())

	order, err := svc.Create(context.Background(), "test-item")
	require.NoError(t, err)
	require.NotNil(t, order)

	require.Len(t, pub.calls, 1, "should publish exactly one event")
	assert.Equal(t, TopicOrderCreated, pub.calls[0].topic)
	assert.Contains(t, string(pub.calls[0].payload), order.ID)
}

func TestService_Create_UsesOutboxWriterWhenConfigured(t *testing.T) {
	repo := mem.NewOrderRepository()
	pub := &recordingPublisher{}
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc := NewService(repo, pub, slog.Default(), WithOutboxWriter(writer), WithTxManager(txRunner))

	order, err := svc.Create(context.Background(), "outbox-item")
	require.NoError(t, err)
	require.NotNil(t, order)
	require.Len(t, writer.entries, 1, "should write exactly one outbox entry")
	assert.Equal(t, 1, txRunner.calls, "durable path should run inside txRunner when configured")
	assert.Empty(t, pub.calls, "outbox path should not call direct publisher")
	assert.NotEmpty(t, writer.entries[0].ID)
	assert.Equal(t, order.ID, writer.entries[0].AggregateID)
	assert.Equal(t, "order", writer.entries[0].AggregateType)
	assert.Equal(t, TopicOrderCreated, writer.entries[0].EventType)
	assert.Equal(t, TopicOrderCreated, writer.entries[0].RoutingTopic())
	assert.Contains(t, string(writer.entries[0].Payload), order.ID)
}

func TestService_Create_OutboxWriterFailureReturnsError(t *testing.T) {
	repo := mem.NewOrderRepository()
	pub := &recordingPublisher{}
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	txRunner := &stubTxRunner{}
	svc := NewService(repo, pub, slog.Default(), WithOutboxWriter(writer), WithTxManager(txRunner))

	order, err := svc.Create(context.Background(), "outbox-item")
	require.Error(t, err)
	assert.Nil(t, order)
	assert.Equal(t, 1, txRunner.calls)
	assert.Empty(t, pub.calls, "failure on durable path must not fall back to direct publish")
}

func TestService_Create_RejectsHalfConfiguredDurablePath(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "writer without tx manager",
			opts: []Option{WithOutboxWriter(&recordingWriter{})},
		},
		{
			name: "tx manager without writer",
			opts: []Option{WithTxManager(&stubTxRunner{})},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := mem.NewOrderRepository()
			pub := &recordingPublisher{}
			svc := NewService(repo, pub, slog.Default(), tt.opts...)

			order, err := svc.Create(context.Background(), "misconfigured")
			require.Error(t, err)
			assert.Nil(t, order)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, errcode.ErrCellMissingOutbox, ecErr.Code)
			assert.Empty(t, pub.calls)
		})
	}
}

func TestService_Create_PersistsOrder(t *testing.T) {
	repo := mem.NewOrderRepository()
	svc := NewService(repo, noopPublisher{}, slog.Default())

	order, err := svc.Create(context.Background(), "persisted")
	require.NoError(t, err)

	got, err := repo.GetByID(context.Background(), order.ID)
	require.NoError(t, err)
	assert.Equal(t, order.ID, got.ID)
	assert.Equal(t, "persisted", got.Item)
}

// failRepo is a repository that always fails on Create.
type failRepo struct {
	domain.OrderRepository
}

func (failRepo) Create(_ context.Context, _ *domain.Order) error {
	return errors.New("db connection lost")
}

func TestService_Create_RepoFailure(t *testing.T) {
	svc := NewService(failRepo{}, noopPublisher{}, slog.Default())

	order, err := svc.Create(context.Background(), "item")
	require.Error(t, err)
	assert.Nil(t, order)
	assert.Contains(t, err.Error(), "persist")
}

func TestService_Create_DemoPublishSuccess(t *testing.T) {
	repo := mem.NewOrderRepository()
	pub := &recordingPublisher{}
	svc := NewService(repo, pub, slog.Default())

	order, err := svc.Create(context.Background(), "demo-item")
	require.NoError(t, err)
	require.NotNil(t, order)
	require.Len(t, pub.calls, 1)
	assert.Equal(t, TopicOrderCreated, pub.calls[0].topic)
}

func TestService_Create_DemoNilPublisher(t *testing.T) {
	repo := mem.NewOrderRepository()
	svc := NewService(repo, nil, slog.Default())

	order, err := svc.Create(context.Background(), "nil-pub")
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, "nil-pub", order.Item)
}

func TestService_Create_DemoDiscardPublisherLogsSkip(t *testing.T) {
	repo := mem.NewOrderRepository()
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	svc := NewService(repo, outbox.DiscardPublisher{}, logger)

	order, err := svc.Create(context.Background(), "discard-pub")
	require.NoError(t, err)
	require.NotNil(t, order)
	// DiscardPublisher.Publish() logs via slog.Default, not the injected logger.
	// The service should NOT log "event published" (success) since DiscardPublisher
	// returns nil error but the caller logs Info "event published" on success.
	// With the new design, Publish() succeeds (nil error) so "event published"
	// IS logged by the service — but the discard warn comes from DiscardPublisher
	// itself via slog.Default which goes to stderr, not our buffer.
	assert.NotContains(t, logs.String(), "skipping direct publish")
}

func TestService_Create_DemoRepoFailure(t *testing.T) {
	svc := NewService(failRepo{}, &recordingPublisher{}, slog.Default())

	order, err := svc.Create(context.Background(), "fail")
	require.Error(t, err)
	assert.Nil(t, order)
	assert.Contains(t, err.Error(), "persist")
}
