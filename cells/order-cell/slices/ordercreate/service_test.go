package ordercreate

import (
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
	"github.com/ghbvf/gocell/pkg/query"
)

// --- test doubles ---

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
		name    string
		item    string
		wantErr bool
		errCode errcode.Code
	}{
		{
			name: "success via outbox path",
			item: "widget",
		},
		{
			name:    "empty item returns validation error",
			item:    "",
			wantErr: true,
			errCode: errcode.ErrValidationFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(mem.NewOrderRepository(), slog.Default(),
				WithOutboxWriter(outbox.NoopWriter{}),
				WithTxManager(persistence.NoopTxRunner{}),
			)

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

func TestService_Create_WritesOutboxEntry(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc := NewService(repo, slog.Default(), WithOutboxWriter(writer), WithTxManager(txRunner))

	order, err := svc.Create(context.Background(), "outbox-item")
	require.NoError(t, err)
	require.NotNil(t, order)
	require.Len(t, writer.entries, 1, "should write exactly one outbox entry")
	assert.Equal(t, 1, txRunner.calls, "should run inside txRunner")
	assert.NotEmpty(t, writer.entries[0].ID)
	assert.Equal(t, order.ID, writer.entries[0].AggregateID)
	assert.Equal(t, "order", writer.entries[0].AggregateType)
	assert.Equal(t, TopicOrderCreated, writer.entries[0].EventType)
	assert.Equal(t, TopicOrderCreated, writer.entries[0].RoutingTopic())
	assert.Contains(t, string(writer.entries[0].Payload), order.ID)
}

func TestService_Create_OutboxWriterFailureReturnsError(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	txRunner := &stubTxRunner{}
	svc := NewService(repo, slog.Default(), WithOutboxWriter(writer), WithTxManager(txRunner))

	order, err := svc.Create(context.Background(), "outbox-item")
	require.Error(t, err)
	assert.Nil(t, order)
	assert.Equal(t, 1, txRunner.calls)

	// Document known limitation: stubTxRunner has no rollback, so the order
	// persists in-memory even though the outbox write failed. With a real
	// postgres TxManager, the entire transaction (including repo.Create)
	// would be rolled back. This assertion captures the current demo-mode
	// behavior and will fail-safe if stubTxRunner gains rollback semantics.
	orders, listErr := repo.List(context.Background(), query.ListParams{Limit: 10})
	require.NoError(t, listErr)
	assert.Len(t, orders, 1, "stubTxRunner: order persists despite outbox failure (no rollback in demo mode)")
}

func TestService_Create_NoopWriterDemoPath(t *testing.T) {
	// Demo mode: NoopWriter validates entries then discards. Same outbox code path.
	repo := mem.NewOrderRepository()
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)

	order, err := svc.Create(context.Background(), "demo-item")
	require.NoError(t, err)
	require.NotNil(t, order)
	assert.Equal(t, "demo-item", order.Item)
}

func TestService_Create_PersistsOrder(t *testing.T) {
	repo := mem.NewOrderRepository()
	svc := NewService(repo, slog.Default(),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)

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
	svc := NewService(failRepo{}, slog.Default(),
		WithOutboxWriter(outbox.NoopWriter{}),
		WithTxManager(persistence.NoopTxRunner{}),
	)

	order, err := svc.Create(context.Background(), "item")
	require.Error(t, err)
	assert.Nil(t, order)
	assert.Contains(t, err.Error(), "persist")
}
