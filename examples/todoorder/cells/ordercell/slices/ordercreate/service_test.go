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
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	createv1 "github.com/ghbvf/gocell/generated/contracts/http/order/create/v1"
)

// testCtx returns a context with an authenticated principal (subject "test-user").
// Create requires a non-empty principal subject so that the created order has a
// non-empty Owner (fail-fast defense-in-depth added in PR-10d Fix-2).
func testCtx() context.Context {
	return auth.TestContext("test-user", []string{"role:customer"})
}

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

func mustEmitter(t testing.TB, w outbox.Writer) outbox.CellEmitter {
	t.Helper()
	emitter, err := outbox.NewWriterEmitter(w)
	require.NoError(t, err)
	return outbox.WrapEmitterForCell(emitter)
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
			svc, err := NewService(clock.Real(), mem.NewOrderRepository(), slog.Default(),
				WithEmitter(outbox.DemoCellEmitter()),
				WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
			)
			require.NoError(t, err)

			resp, createErr := svc.Create(testCtx(), &createv1.Request{Item: tt.item})
			if tt.wantErr {
				require.Error(t, createErr)
				var ecErr *errcode.Error
				require.ErrorAs(t, createErr, &ecErr)
				assert.Equal(t, tt.errCode, ecErr.Code)
				assert.Nil(t, resp)
			} else {
				require.NoError(t, createErr)
				require.NotNil(t, resp)
				r, ok := resp.(createv1.Create201JSONResponse)
				require.True(t, ok, "expected Create201JSONResponse")
				require.NotNil(t, r.Data)
				assert.Equal(t, tt.item, r.Data.Item)
				assert.Equal(t, "pending", r.Data.Status)
				assert.NotEmpty(t, r.Data.ID)
			}
		})
	}
}

func TestService_Create_WritesOutboxEntry(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithEmitter(mustEmitter(t, writer)),
		WithTxManager(persistence.WrapForCell(txRunner)))
	require.NoError(t, err)

	resp, err := svc.Create(testCtx(), &createv1.Request{Item: "outbox-item"})
	require.NoError(t, err)
	require.NotNil(t, resp)
	r, ok := resp.(createv1.Create201JSONResponse)
	require.True(t, ok, "expected Create201JSONResponse")
	require.NotNil(t, r.Data)
	require.Len(t, writer.entries, 1, "should write exactly one outbox entry")
	assert.Equal(t, 1, txRunner.calls, "should run inside txRunner")
	assert.NotEmpty(t, writer.entries[0].ID())
	assert.Equal(t, r.Data.ID, writer.entries[0].AggregateID())
	assert.Equal(t, "order", writer.entries[0].AggregateType())
	assert.Equal(t, TopicOrderCreated, writer.entries[0].EventType())
	assert.Equal(t, TopicOrderCreated, writer.entries[0].RoutingTopic())
	assert.Contains(t, string(writer.entries[0].Payload()), r.Data.ID)
}

func TestService_Create_OutboxWriterFailureReturnsError(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	txRunner := &stubTxRunner{}
	svc, err := NewService(clock.Real(), repo, slog.Default(), WithEmitter(mustEmitter(t, writer)),
		WithTxManager(persistence.WrapForCell(txRunner)))
	require.NoError(t, err)

	resp, createErr := svc.Create(testCtx(), &createv1.Request{Item: "outbox-item"})
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
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.DemoCellEmitter()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(testCtx(), &createv1.Request{Item: "demo-item"})
	require.NoError(t, createErr)
	require.NotNil(t, resp)
	rDemo, ok := resp.(createv1.Create201JSONResponse)
	require.True(t, ok, "expected Create201JSONResponse")
	require.NotNil(t, rDemo.Data)
	assert.Equal(t, "demo-item", rDemo.Data.Item)
}

func TestService_Create_PersistsOrder(t *testing.T) {
	repo := mem.NewOrderRepository()
	svc, err := NewService(clock.Real(), repo, slog.Default(),
		WithEmitter(outbox.DemoCellEmitter()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(testCtx(), &createv1.Request{Item: "persisted"})
	require.NoError(t, createErr)
	require.NotNil(t, resp)
	rPersist, ok := resp.(createv1.Create201JSONResponse)
	require.True(t, ok, "expected Create201JSONResponse")
	require.NotNil(t, rPersist.Data)

	got, err := repo.GetByID(context.Background(), rPersist.Data.ID)
	require.NoError(t, err)
	assert.Equal(t, rPersist.Data.ID, got.ID)
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
	svc, err := NewService(clock.Real(), failRepo{}, slog.Default(),
		WithEmitter(outbox.DemoCellEmitter()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(testCtx(), &createv1.Request{Item: "item"})
	require.Error(t, createErr)
	assert.Nil(t, resp)
	assert.Contains(t, createErr.Error(), "persist")
}

// TestService_Create_NoPrincipal verifies that Create returns ErrAuthUnauthorized
// when the context carries no authenticated principal (defense-in-depth: the
// create gate guarantees a principal in production, but a missing-principal
// context must not produce an orphaned order with Owner="").
func TestService_Create_NoPrincipal(t *testing.T) {
	svc, err := NewService(clock.Real(), mem.NewOrderRepository(), slog.Default(),
		WithEmitter(outbox.DemoCellEmitter()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
	)
	require.NoError(t, err)

	resp, createErr := svc.Create(context.Background(), &createv1.Request{Item: "item"})
	require.Error(t, createErr)
	assert.Nil(t, resp)
	var ecErr *errcode.Error
	require.ErrorAs(t, createErr, &ecErr)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ecErr.Code)
}

// TestNewService_NilDep is a table-driven test verifying that NewService rejects
// each required nil dependency (repo, txRunner) with a non-nil errcode.Error.
func TestNewService_NilDep(t *testing.T) {
	tests := []struct {
		name     string
		repo     domain.OrderRepository
		opts     []Option
		wantCode errcode.Code
		wantMsg  string
	}{
		{
			name: "nil repo",
			repo: nil,
			opts: []Option{
				WithEmitter(outbox.DemoCellEmitter()),
				WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
			},
			wantCode: errcode.ErrCellInvalidConfig,
			wantMsg:  "repo required",
		},
		{
			name:     "nil txRunner",
			repo:     mem.NewOrderRepository(),
			opts:     []Option{WithEmitter(outbox.DemoCellEmitter())},
			wantCode: errcode.ErrCellInvalidConfig,
			wantMsg:  "TxRunner required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(clock.Real(), tt.repo, slog.Default(), tt.opts...)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, tt.wantCode, ecErr.Code)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}

// TestService_NilTxRunner_FailsFast verifies that NewService rejects nil TxRunner.
func TestService_NilTxRunner_FailsFast(t *testing.T) {
	_, err := NewService(clock.Real(), mem.NewOrderRepository(), slog.Default(),
		WithEmitter(outbox.DemoCellEmitter()),
		// No WithTxManager — txRunner remains nil.
	)
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}
