package orderconfirm

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	confirmv1 "github.com/ghbvf/gocell/generated/contracts/http/order/confirm/v1"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
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

// seedOrder creates a pending order in repo and returns its ID.
func seedOrder(t testing.TB, repo *mem.OrderRepository) string {
	t.Helper()
	order := &domain.Order{
		ID:     "ord-test-seed-001",
		Item:   "widget",
		Status: domain.StatusPending,
	}
	err := repo.Create(context.Background(), order)
	require.NoError(t, err)
	return order.ID
}

func newTestService(t testing.TB, repo domain.OrderRepository, writer *recordingWriter, txRunner *stubTxRunner) *Service {
	t.Helper()
	svc, err := NewService(repo, slog.Default(),
		WithEmitter(mustEmitter(t, writer)),
		WithTxManager(persistence.WrapForCell(txRunner)),
	)
	require.NoError(t, err)
	return svc
}

// TestService_Confirm_Success tests successful order confirmation.
func TestService_Confirm_Success(t *testing.T) {
	repo := mem.NewOrderRepository()
	orderID := seedOrder(t, repo)
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc := newTestService(t, repo, writer, txRunner)

	resp, err := svc.Confirm(context.Background(), &confirmv1.Request{ID: orderID, Status: domain.StatusConfirmed})
	require.NoError(t, err)
	require.NotNil(t, resp)

	r, ok := resp.(confirmv1.Confirm200JSONResponse)
	require.True(t, ok, "expected Confirm200JSONResponse, got %T", resp)
	assert.Equal(t, orderID, r.Data.ID)
	assert.Equal(t, domain.StatusConfirmed, r.Data.Status)

	// Verify repo state updated
	order, err := repo.GetByID(context.Background(), orderID)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusConfirmed, order.Status)

	// Verify exactly one outbox entry emitted
	require.Len(t, writer.entries, 1)
	entry := writer.entries[0]
	assert.Equal(t, orderID, entry.AggregateID)
	assert.Equal(t, "order", entry.AggregateType)
	assert.Equal(t, TopicOrderStatusChanged, entry.EventType)
	assert.Equal(t, TopicOrderStatusChanged, entry.RoutingTopic())
	assert.Contains(t, string(entry.Payload), `"oldStatus":"pending"`)
	assert.Contains(t, string(entry.Payload), `"newStatus":"confirmed"`)
	assert.Contains(t, string(entry.Payload), orderID)

	// Verify tx was used
	assert.Equal(t, 1, txRunner.calls)
}

// TestService_Confirm_AlreadyConfirmed tests that confirming an already-confirmed order returns 409.
func TestService_Confirm_AlreadyConfirmed(t *testing.T) {
	repo := mem.NewOrderRepository()
	// Seed a confirmed order
	order := &domain.Order{
		ID:     "ord-already-confirmed",
		Item:   "widget",
		Status: domain.StatusConfirmed,
	}
	err := repo.Create(context.Background(), order)
	require.NoError(t, err)

	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc := newTestService(t, repo, writer, txRunner)

	resp, err := svc.Confirm(context.Background(), &confirmv1.Request{ID: order.ID, Status: domain.StatusConfirmed})
	require.NoError(t, err, "business 4xx must be returned as typed struct, not error")
	require.NotNil(t, resp)

	_, ok := resp.(confirmv1.Confirm409ErrorResponse)
	require.True(t, ok, "expected Confirm409ErrorResponse, got %T", resp)

	// No events emitted
	assert.Empty(t, writer.entries)
}

// TestService_Confirm_NotFound tests that a missing order returns 404.
func TestService_Confirm_NotFound(t *testing.T) {
	repo := mem.NewOrderRepository()
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc := newTestService(t, repo, writer, txRunner)

	resp, err := svc.Confirm(context.Background(), &confirmv1.Request{ID: "ord-does-not-exist", Status: domain.StatusConfirmed})
	require.NoError(t, err, "business 4xx must be returned as typed struct, not error")
	require.NotNil(t, resp)

	resp404, ok := resp.(confirmv1.Confirm404ErrorResponse)
	require.True(t, ok, "expected Confirm404ErrorResponse, got %T", resp)
	// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01: every _NotFound test
	// must assert a typed errcode.Err*NotFound via the errcodetest funnel.
	errcodetest.AssertCode(t, &resp404.Body, errcode.ErrOrderNotFound)

	// No events emitted
	assert.Empty(t, writer.entries)
}

// TestService_Confirm_InvalidStatus tests that a non-"confirmed" status value returns 400.
func TestService_Confirm_InvalidStatus(t *testing.T) {
	repo := mem.NewOrderRepository()
	_ = seedOrder(t, repo)
	writer := &recordingWriter{}
	txRunner := &stubTxRunner{}
	svc := newTestService(t, repo, writer, txRunner)

	tests := []struct {
		name   string
		status string
	}{
		{"shipped", "shipped"},
		{"pending", "pending"},
		{"empty", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := svc.Confirm(context.Background(), &confirmv1.Request{ID: "ord-test-seed-001", Status: tt.status})
			require.NoError(t, err, "business 4xx must be returned as typed struct, not error")
			require.NotNil(t, resp)

			_, ok := resp.(confirmv1.Confirm400ErrorResponse)
			require.True(t, ok, "expected Confirm400ErrorResponse for status=%q, got %T", tt.status, resp)

			assert.Empty(t, writer.entries)
		})
	}
}

// TestService_Confirm_EmitFailure_ReturnsError verifies that an outbox emit failure
// inside the transaction causes Confirm to return an error.
//
// Note: stubTxRunner has no real rollback semantics — it executes fn directly
// without a database transaction. Actual atomicity (status change rolled back on
// emit failure) requires testcontainers + a real PG transaction. This test
// documents the stub boundary and verifies the error propagation path only.
func TestService_Confirm_EmitFailure_ReturnsError(t *testing.T) {
	repo := mem.NewOrderRepository()
	orderID := seedOrder(t, repo)

	// recordingWriter that fails on Write — simulates emit failure inside tx
	writer := &recordingWriter{err: errors.New("outbox unavailable")}
	txRunner := &stubTxRunner{}
	svc := newTestService(t, repo, writer, txRunner)

	_, err := svc.Confirm(context.Background(), &confirmv1.Request{ID: orderID, Status: domain.StatusConfirmed})
	// emit failure inside RunInTx → RunInTx returns error → Confirm returns error
	require.Error(t, err)

	// No outbox entry was captured (Write failed before appending).
	assert.Empty(t, writer.entries)

	// stub has no rollback, so status IS changed — document the stub boundary:
	// with a real PG transaction the status would remain pending on emit failure.
	order, getErr := repo.GetByID(context.Background(), orderID)
	require.NoError(t, getErr)
	assert.Equal(t, domain.StatusConfirmed, order.Status,
		"stub has no rollback: status was updated before emit failure; real PG tx would roll this back")
}

// TestNewService_NilDep verifies required-dep nil guard.
func TestNewService_NilDep(t *testing.T) {
	tests := []struct {
		name     string
		repo     domain.OrderRepository
		opts     []Option
		wantCode errcode.Code
		wantMsg  string
	}{
		{
			name:     "nil repo",
			repo:     nil,
			opts:     []Option{WithEmitter(mustEmitter(t, outbox.NoopWriter{})), WithTxManager(persistence.WrapForCell(&stubTxRunner{}))},
			wantCode: errcode.ErrCellInvalidConfig,
			wantMsg:  "repo required",
		},
		{
			name:     "nil txRunner",
			repo:     mem.NewOrderRepository(),
			opts:     []Option{WithEmitter(mustEmitter(t, outbox.NoopWriter{}))},
			wantCode: errcode.ErrCellInvalidConfig,
			wantMsg:  "TxRunner required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewService(tt.repo, slog.Default(), tt.opts...)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr)
			assert.Equal(t, tt.wantCode, ecErr.Code)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}
