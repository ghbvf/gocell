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

// newTestService is kept for backwards-compatible single-service tests.
func newTestService(t *testing.T) *placeorder.Service {
	t.Helper()
	return newTestServiceBundle(t).svc
}

func TestService_PlaceOrder_HappyPath(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	id, err := svc.PlaceOrder(ctx, "widget", 1000, false)
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.Contains(t, id, "ord-")
}

func TestService_PlaceOrder_MultipleOrders(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	id1, err := svc.PlaceOrder(ctx, "widget", 1000, false)
	require.NoError(t, err)

	id2, err := svc.PlaceOrder(ctx, "gadget", 2000, false)
	require.NoError(t, err)

	require.NotEqual(t, id1, id2, "each order must have a unique ID")
}

func TestService_PlaceOrder_PaymentFailFlag(t *testing.T) {
	t.Parallel()
	svc := newTestService(t)
	ctx := context.Background()

	id, err := svc.PlaceOrder(ctx, "widget", 500, true)
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

	id, err := bundle.svc.PlaceOrder(ctx, "widget", 1299, false)
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// Load must return nil error — the instance was enrolled.
	// No coordinator is running so events may be empty but the instance exists.
	events, err := bundle.jrnl.Load(ctx, idutil.SafeID(id))
	require.NoError(t, err, "saga instance must be enrolled (Load must not return NotFound)")
	// events may be nil or empty at this point (coordinator not running);
	// what matters is that the instance is queryable without error.
	_ = events
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
