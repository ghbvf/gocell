package ordercreate

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	createv1 "github.com/ghbvf/gocell/generated/contracts/http/order/create/v1"
	"github.com/ghbvf/gocell/kernel/clock"
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

func mustEmitter(t testing.TB, w outbox.Writer) outbox.Emitter {
	t.Helper()
	emitter, err := outbox.NewWriterEmitter(w)
	require.NoError(t, err)
	return emitter
}

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
			svc, err := NewService(mem.NewOrderRepository(), slog.Default(),
				WithEmitter(mustEmitter(t, outbox.NoopWriter{})),
				WithTxManager(&stubTxRunner{}),
				WithClock(clock.Real()),
			)
			require.NoError(t, err)

			resp, createErr := svc.Create(context.Background(), &createv1.Request{Item: tt.item})
			if tt.wantErr {
				require.Error(t, createErr)
				var ecErr *errcode.Error
				require.ErrorAs(t, createErr, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, createErr)
				require.NotNil(t, resp)
				require.NotNil(t, resp.Data)
				assert.Equal(t, tt.item, resp.Data.Item)
				assert.Equal(t, "pending", resp.Data.Status)
				assert.NotEmpty(t, resp.Data.Id)
			}
		})
	}
}

func TestService_Create_WritesOutboxEntry(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc, err := NewService(repo, slog.Default(), WithEmitter(mustEmitter(t, writer)), WithTxManager(txRunner), WithClock(clock.Real()))
	require.NoError(t, err)

	resp, err := svc.Create(context.Background(), &createv1.Request{Item: "outbox-item"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Data)
	require.Len(t, writer.entries, 1, "should write exactly one outbox entry")
	assert.Equal(t, 1, txRunner.calls, "should run inside txRunner")
	assert.NotEmpty(t, writer.entries[0].ID)
	assert.Equal(t, resp.Data.Id, writer.entries[0].AggregateID)
	assert.Equal(t, "order", writer.entries[0].AggregateType)
	assert.Equal(t, TopicOrderCreated, writer.entries[0].EventType)
	assert.Equal(t, TopicOrderCreated, writer.entries[0].RoutingTopic())
	assert.Contains(t, string(writer.entries[0].Payload), resp.Data.Id)
}

func TestService_Create_OutboxWriterFailureReturnsError(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	txRunner := &stubTxRunner{}
	svc, err := NewService(repo, slog.Default(), WithEmitter(mustEmitter(t, writer)), WithTxManager(txRunner), WithClock(clock.Real()))
	require.NoError(t, err)

	resp, createErr := svc.Create(context.Background(), &createv1.Request{Item: "outbox-item"})
	require.Error(t, createErr)
	assert.Nil(t, resp)
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
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(mustEmitter(t, outbox.NoopWriter{})),
		WithTxManager(&stubTxRunner{}),
		WithClock(clock.Real()),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(context.Background(), &createv1.Request{Item: "demo-item"})
	require.NoError(t, createErr)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Data)
	assert.Equal(t, "demo-item", resp.Data.Item)
}

func TestService_Create_PersistsOrder(t *testing.T) {
	repo := mem.NewOrderRepository()
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(mustEmitter(t, outbox.NoopWriter{})),
		WithTxManager(&stubTxRunner{}),
		WithClock(clock.Real()),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(context.Background(), &createv1.Request{Item: "persisted"})
	require.NoError(t, createErr)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Data)

	got, err := repo.GetByID(context.Background(), resp.Data.Id)
	require.NoError(t, err)
	assert.Equal(t, resp.Data.Id, got.ID)
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
	svc, err := NewService(failRepo{}, slog.Default(),
		WithEmitter(mustEmitter(t, outbox.NoopWriter{})),
		WithTxManager(&stubTxRunner{}),
		WithClock(clock.Real()),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(context.Background(), &createv1.Request{Item: "item"})
	require.Error(t, createErr)
	assert.Nil(t, resp)
	assert.Contains(t, createErr.Error(), "persist")
}

// TestService_NilTxRunner_FailsFast verifies that NewService rejects nil TxRunner.
func TestService_NilTxRunner_FailsFast(t *testing.T) {
	_, err := NewService(mem.NewOrderRepository(), slog.Default(),
		WithEmitter(mustEmitter(t, outbox.NoopWriter{})),
		// No WithTxManager — txRunner remains nil.
		WithClock(clock.Real()),
	)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}
