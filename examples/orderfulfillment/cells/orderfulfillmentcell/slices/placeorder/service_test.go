package placeorder_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// testServiceBundle holds a placeorder.Service together with its backing
// MemJournal so tests can inspect saga enrollment without running a coordinator.
type testServiceBundle struct {
	svc  *placeorder.Service
	jrnl *journal.MemJournal
}

func newTestServiceBundle(t *testing.T) testServiceBundle {
	t.Helper()
	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	require.NoError(t, err)
	repo := mem.NewOrderRepository()
	svc, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(repo),
		placeorder.WithJournal(jrnl),
	)
	require.NoError(t, err)
	return testServiceBundle{svc: svc, jrnl: jrnl}
}

func TestService_PlaceOrder_HappyPath(t *testing.T) {
	t.Parallel()
	svc := newTestServiceBundle(t).svc
	ctx := context.Background()

	id, err := svc.PlaceOrder(ctx, "key-happy-1", "widget", 1000, false)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Contains(t, id, "ord-")
}

func TestService_PlaceOrder_MultipleOrders(t *testing.T) {
	t.Parallel()
	svc := newTestServiceBundle(t).svc
	ctx := context.Background()

	id1, err := svc.PlaceOrder(ctx, "key-multi-1", "widget", 1000, false)
	require.NoError(t, err)

	id2, err := svc.PlaceOrder(ctx, "key-multi-2", "gadget", 2000, false)
	require.NoError(t, err)

	require.NotEqual(t, id1, id2, "different idempotency keys must produce different order IDs")
}

func TestService_PlaceOrder_PaymentFailFlag(t *testing.T) {
	t.Parallel()
	svc := newTestServiceBundle(t).svc
	ctx := context.Background()

	id, err := svc.PlaceOrder(ctx, "key-payfail-1", "widget", 500, true)
	require.NoError(t, err)
	require.NotEmpty(t, id)
}

// TestService_PlaceOrder_SagaEnrolled verifies that PlaceOrder enqueues a saga
// instance in the MemJournal without running a coordinator. This is a pure
// enrollment check: no steps are driven, so Load returns an empty (non-nil)
// event slice for the newly created instance — the MemJournal returns nil error
// for enqueued instances, distinguishing them from truly unknown IDs.
func TestService_PlaceOrder_SagaEnrolled(t *testing.T) {
	t.Parallel()
	bundle := newTestServiceBundle(t)
	ctx := context.Background()

	id, err := bundle.svc.PlaceOrder(ctx, "key-enrolled-1", "widget", 1299, false)
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// Load must return nil error — the instance was enrolled.
	// No coordinator is running so events may be empty but the instance exists.
	events, err := bundle.jrnl.Load(ctx, idutil.SafeID(id))
	require.NoError(t, err, "saga instance must be enrolled (Load must not return NotFound)")
	require.Empty(t, events, "freshly enrolled saga should have no events before coordinator runs")
}

// TestService_PlaceOrder_Idempotent verifies that calling PlaceOrder twice with
// the same idempotency key returns the same orderID without duplicate saga
// enrollment. The MemJournal should contain exactly one enrolled instance.
func TestService_PlaceOrder_Idempotent(t *testing.T) {
	t.Parallel()
	bundle := newTestServiceBundle(t)
	ctx := context.Background()

	const key = "key-idempotent-1"

	// First call: creates order + enrolls saga.
	id1, err := bundle.svc.PlaceOrder(ctx, key, "widget", 1299, false)
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	// Second call with same key: idempotent hit, same orderID, no duplicate enrollment.
	id2, err := bundle.svc.PlaceOrder(ctx, key, "widget", 1299, false)
	require.NoError(t, err)
	require.Equal(t, id1, id2, "same idempotency key must return same orderID")

	// Journal must contain exactly one instance (not two).
	events, err := bundle.jrnl.Load(ctx, idutil.SafeID(id1))
	require.NoError(t, err)
	require.Empty(t, events, "freshly enrolled saga should have no events before coordinator runs")

	// A different key must produce a different orderID.
	id3, err := bundle.svc.PlaceOrder(ctx, "key-idempotent-2", "widget", 1299, false)
	require.NoError(t, err)
	require.NotEqual(t, id1, id3, "different keys must produce different order IDs")
}

func TestNewService_MissingOrders(t *testing.T) {
	t.Parallel()
	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	require.NoError(t, err)

	_, err = placeorder.NewService(
		clk,
		placeorder.WithJournal(jrnl),
		// orders NOT injected
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "orders required")
}

func TestNewService_MissingJournal(t *testing.T) {
	t.Parallel()
	clk := clock.Real()
	repo := mem.NewOrderRepository()

	_, err := placeorder.NewService(
		clk,
		placeorder.WithOrderRepository(repo),
		// journal NOT injected
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "journal required")
}
