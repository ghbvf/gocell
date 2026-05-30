package placeorder_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	placeorder "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/saga/journal"
)

func newTestService(t *testing.T) *placeorder.Service {
	t.Helper()
	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	require.NoError(t, err)
	repo := mem.NewOrderRepository()
	svc, err := placeorder.NewService(clk,
		placeorder.WithOrderRepository(repo),
		placeorder.WithJournal(jrnl),
	)
	require.NoError(t, err)
	return svc
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

func TestNewService_MissingOrders(t *testing.T) {
	t.Parallel()
	clk := clock.Real()
	jrnl, err := journal.NewMemJournal(clk)
	require.NoError(t, err)

	_, err = placeorder.NewService(clk,
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

	_, err := placeorder.NewService(clk,
		placeorder.WithOrderRepository(repo),
		// journal NOT injected
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "journal required")
}
