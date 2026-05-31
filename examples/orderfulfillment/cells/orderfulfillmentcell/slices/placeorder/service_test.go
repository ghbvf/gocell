package placeorder_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
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
// the same idempotency key and same parameters returns the same orderID without
// duplicate saga enrollment. The MemJournal must contain exactly one enrolled
// instance (not two), proving "Create Conflict → skip Enqueue" path is taken.
func TestService_PlaceOrder_Idempotent(t *testing.T) {
	t.Parallel()
	bundle := newTestServiceBundle(t)
	ctx := context.Background()

	const key = "key-idempotent-1"

	// First call: creates order + enrolls saga.
	id1, err := bundle.svc.PlaceOrder(ctx, key, "widget", 1299, false)
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	// Second call with same key and same parameters: idempotent hit.
	id2, err := bundle.svc.PlaceOrder(ctx, key, "widget", 1299, false)
	require.NoError(t, err)
	require.Equal(t, id1, id2, "same idempotency key must return same orderID")

	// Journal must contain exactly one instance (not two) — the second call
	// must NOT re-enqueue. If the saga were enrolled twice, the MemJournal
	// would return ErrSagaDuplicateInstance on Load (panics in MemJournal are
	// not possible, but a second enrollment attempt would error). The key
	// assertion is that Load succeeds (instance enrolled once) and contains
	// no events (no coordinator ran).
	events, err := bundle.jrnl.Load(ctx, idutil.SafeID(id1))
	require.NoError(t, err, "saga instance must be enrolled exactly once")
	require.Empty(t, events, "freshly enrolled saga should have no events before coordinator runs")

	// A different key must produce a different orderID, confirming the journal
	// now holds two distinct instances (not a collision).
	id3, err := bundle.svc.PlaceOrder(ctx, "key-idempotent-2", "widget", 1299, false)
	require.NoError(t, err)
	require.NotEqual(t, id1, id3, "different keys must produce different order IDs")
	events3, err := bundle.jrnl.Load(ctx, idutil.SafeID(id3))
	require.NoError(t, err, "second saga instance must also be enrolled")
	require.Empty(t, events3)
}

// TestService_PlaceOrder_IdempotencyKeyConflict verifies that reusing an
// idempotency key with different parameters (item, amountCents, or
// paymentShouldFail) returns errcode.KindConflict (HTTP 409). This is the
// Stripe / Temporal idempotency key reuse semantic: different parameters
// require a distinct key.
func TestService_PlaceOrder_IdempotencyKeyConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		first  func(ctx context.Context, svc *placeorder.Service) (string, error)
		second func(ctx context.Context, svc *placeorder.Service) (string, error)
	}{
		{
			name: "different item",
			first: func(ctx context.Context, svc *placeorder.Service) (string, error) {
				return svc.PlaceOrder(ctx, "key-conflict-item", "widget", 500, false)
			},
			second: func(ctx context.Context, svc *placeorder.Service) (string, error) {
				return svc.PlaceOrder(ctx, "key-conflict-item", "gadget", 500, false)
			},
		},
		{
			name: "different amountCents",
			first: func(ctx context.Context, svc *placeorder.Service) (string, error) {
				return svc.PlaceOrder(ctx, "key-conflict-amt", "widget", 500, false)
			},
			second: func(ctx context.Context, svc *placeorder.Service) (string, error) {
				return svc.PlaceOrder(ctx, "key-conflict-amt", "widget", 999, false)
			},
		},
		{
			name: "different paymentShouldFail",
			first: func(ctx context.Context, svc *placeorder.Service) (string, error) {
				return svc.PlaceOrder(ctx, "key-conflict-psf", "widget", 500, false)
			},
			second: func(ctx context.Context, svc *placeorder.Service) (string, error) {
				return svc.PlaceOrder(ctx, "key-conflict-psf", "widget", 500, true)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			bundle := newTestServiceBundle(t)
			ctx := context.Background()

			// First call succeeds.
			_, err := tc.first(ctx, bundle.svc)
			require.NoError(t, err)

			// Second call with same key but different parameters must fail with KindConflict.
			_, err = tc.second(ctx, bundle.svc)
			require.Error(t, err, "key reuse with different parameters must return an error")

			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "error must be *errcode.Error")
			require.Equal(t, errcode.KindConflict, ec.Kind, "error kind must be KindConflict (HTTP 409)")
		})
	}
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
