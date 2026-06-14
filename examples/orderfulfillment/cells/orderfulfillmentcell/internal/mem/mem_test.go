package mem_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

func TestOrderRepository(t *testing.T) {
	t.Parallel()

	t.Run("create_and_get", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		order := &domain.Order{ID: "ord-1", Item: "widget", AmountCents: 1000, CreatedAt: time.Now()}
		require.NoError(t, r.Create(context.Background(), order), "Create")

		got, err := r.GetByID(context.Background(), "ord-1")
		require.NoError(t, err, "GetByID")
		assert.Equal(t, order.ID, got.ID)
		assert.Equal(t, order.Item, got.Item)
		assert.Equal(t, order.AmountCents, got.AmountCents)
	})

	t.Run("get_not_found", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		_, err := r.GetByID(context.Background(), "missing")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.KindNotFound, ec.Kind)
	})

	t.Run("create_returns_copy", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		order := &domain.Order{ID: "ord-2", Item: "gadget", AmountCents: 500, CreatedAt: time.Now()}
		require.NoError(t, r.Create(context.Background(), order), "Create")
		// Mutate the original; the stored copy must be unaffected.
		order.AmountCents = 99999
		got, err := r.GetByID(context.Background(), "ord-2")
		require.NoError(t, err, "GetByID")
		assert.EqualValues(t, 500, got.AmountCents, "Create must store a copy, not a reference")
	})

	t.Run("create_empty_id_returns_error", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		order := &domain.Order{ID: "", Item: "widget", AmountCents: 100, CreatedAt: time.Now()}
		var ec *errcode.Error
		require.ErrorAs(t, r.Create(context.Background(), order), &ec)
		assert.Equal(t, errcode.KindInvalid, ec.Kind)
	})
}

func TestInventoryStore(t *testing.T) {
	t.Parallel()

	t.Run("reserve_and_release", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{"widget": 2})

		resID, err := s.Reserve(context.Background(), "ord-1", "widget")
		require.NoError(t, err, "Reserve")
		assert.Equal(t, "res-ord-1", resID)
		assert.Equal(t, 1, s.Available("widget"), "available after reserve")

		require.NoError(t, s.Release(context.Background(), "ord-1"), "Release")
		assert.Equal(t, 2, s.Available("widget"), "available after release")
	})

	t.Run("release_idempotent", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{"widget": 1})
		require.NoError(t, s.Release(context.Background(), "unknown"), "Release unknown must be nil")

		_, err := s.Reserve(context.Background(), "ord-1", "widget")
		require.NoError(t, err, "Reserve setup")
		require.NoError(t, s.Release(context.Background(), "ord-1"), "Release setup")
		require.NoError(t, s.Release(context.Background(), "ord-1"), "double Release must be nil")
	})

	t.Run("reserve_out_of_stock", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{"widget": 0})
		_, err := s.Reserve(context.Background(), "ord-1", "widget")
		require.Error(t, err, "expected out-of-stock error")
	})

	t.Run("reserve_unknown_item", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{})
		_, err := s.Reserve(context.Background(), "ord-1", "nonexistent")
		require.Error(t, err, "expected unknown-item error")
	})
}

func TestPaymentStore(t *testing.T) {
	t.Parallel()

	t.Run("charge_and_get", func(t *testing.T) {
		t.Parallel()
		s := mem.NewPaymentStore()
		payID, err := s.Charge(context.Background(), "ord-1", 1500)
		require.NoError(t, err, "Charge")
		assert.Equal(t, "pay-ord-1", payID)

		got, ok := s.Get("ord-1")
		assert.True(t, ok)
		assert.Equal(t, "pay-ord-1", got)
	})

	t.Run("refund_removes_payment", func(t *testing.T) {
		t.Parallel()
		s := mem.NewPaymentStore()
		_, err := s.Charge(context.Background(), "ord-1", 1000)
		require.NoError(t, err, "Charge setup")
		require.NoError(t, s.Refund(context.Background(), "ord-1"), "Refund")

		_, ok := s.Get("ord-1")
		assert.False(t, ok, "payment still present after refund")
	})

	t.Run("refund_idempotent", func(t *testing.T) {
		t.Parallel()
		s := mem.NewPaymentStore()
		require.NoError(t, s.Refund(context.Background(), "unknown"), "Refund unknown must be nil")

		_, err := s.Charge(context.Background(), "ord-2", 200)
		require.NoError(t, err, "Charge setup")
		require.NoError(t, s.Refund(context.Background(), "ord-2"), "Refund setup")
		require.NoError(t, s.Refund(context.Background(), "ord-2"), "double Refund must be nil")
	})
}

func TestShipmentStore(t *testing.T) {
	t.Parallel()

	t.Run("create_and_get", func(t *testing.T) {
		t.Parallel()
		s := mem.NewShipmentStore()
		shipID, err := s.CreateShipment(context.Background(), "ord-1")
		require.NoError(t, err, "CreateShipment")
		assert.Equal(t, "ship-ord-1", shipID)

		got, ok := s.Get("ord-1")
		assert.True(t, ok)
		assert.Equal(t, "ship-ord-1", got)
	})

	t.Run("cancel_removes_shipment", func(t *testing.T) {
		t.Parallel()
		s := mem.NewShipmentStore()
		_, err := s.CreateShipment(context.Background(), "ord-1")
		require.NoError(t, err, "CreateShipment setup")
		require.NoError(t, s.CancelShipment(context.Background(), "ord-1"), "CancelShipment")

		_, ok := s.Get("ord-1")
		assert.False(t, ok, "shipment still present after cancel")
	})

	t.Run("cancel_idempotent", func(t *testing.T) {
		t.Parallel()
		s := mem.NewShipmentStore()
		require.NoError(t, s.CancelShipment(context.Background(), "unknown"), "CancelShipment unknown must be nil")

		_, err := s.CreateShipment(context.Background(), "ord-2")
		require.NoError(t, err, "CreateShipment setup")
		require.NoError(t, s.CancelShipment(context.Background(), "ord-2"), "CancelShipment setup")
		require.NoError(t, s.CancelShipment(context.Background(), "ord-2"), "double CancelShipment must be nil")
	})
}

// TestInventoryStore_ConcurrentReserveRelease verifies that concurrent
// Reserve/Release operations on InventoryStore are data-race-free (run with
// -race). With one widget per order, every reserve succeeds and every release is
// idempotent, so no operation may error — each goroutine records its result in a
// distinct slot (no shared write) and the test goroutine asserts after Wait.
func TestInventoryStore_ConcurrentReserveRelease(t *testing.T) {
	t.Parallel()
	const goroutines = 20
	s := mem.NewInventoryStore(map[string]int{"widget": goroutines})

	reserveErrs := make([]error, goroutines)
	releaseErrs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := range goroutines {
		orderID := "ord-" + string(rune('A'+i))
		go func(idx int, id string) {
			defer wg.Done()
			_, reserveErrs[idx] = s.Reserve(context.Background(), id, "widget")
		}(i, orderID)
		go func(idx int, id string) {
			defer wg.Done()
			releaseErrs[idx] = s.Release(context.Background(), id)
		}(i, orderID)
	}
	wg.Wait()

	for i := range goroutines {
		require.NoErrorf(t, reserveErrs[i], "Reserve[%d]", i)
		require.NoErrorf(t, releaseErrs[i], "Release[%d]", i)
	}
	// Inventory invariant after the concurrent phase: available stays within
	// [0, initial stock]. This also exercises Available concurrently with no
	// in-flight writers remaining.
	avail := s.Available("widget")
	assert.GreaterOrEqual(t, avail, 0)
	assert.LessOrEqual(t, avail, goroutines)
}

// TestPaymentStore_ConcurrentChargeRefund verifies that concurrent Charge/Refund
// operations on PaymentStore are data-race-free. Charge always records and
// Refund is idempotent, so neither may error.
func TestPaymentStore_ConcurrentChargeRefund(t *testing.T) {
	t.Parallel()
	const goroutines = 20
	s := mem.NewPaymentStore()

	chargeErrs := make([]error, goroutines)
	refundErrs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := range goroutines {
		orderID := "ord-" + string(rune('A'+i))
		go func(idx int, id string) {
			defer wg.Done()
			_, chargeErrs[idx] = s.Charge(context.Background(), id, 100)
		}(i, orderID)
		go func(idx int, id string) {
			defer wg.Done()
			refundErrs[idx] = s.Refund(context.Background(), id)
		}(i, orderID)
	}
	wg.Wait()

	for i := range goroutines {
		require.NoErrorf(t, chargeErrs[i], "Charge[%d]", i)
		require.NoErrorf(t, refundErrs[i], "Refund[%d]", i)
	}
}

// TestShipmentStore_ConcurrentCreateCancel verifies that concurrent
// CreateShipment/CancelShipment operations on ShipmentStore are data-race-free.
// CreateShipment always records and CancelShipment is idempotent, so neither may
// error.
func TestShipmentStore_ConcurrentCreateCancel(t *testing.T) {
	t.Parallel()
	const goroutines = 20
	s := mem.NewShipmentStore()

	createErrs := make([]error, goroutines)
	cancelErrs := make([]error, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)
	for i := range goroutines {
		orderID := "ord-" + string(rune('A'+i))
		go func(idx int, id string) {
			defer wg.Done()
			_, createErrs[idx] = s.CreateShipment(context.Background(), id)
		}(i, orderID)
		go func(idx int, id string) {
			defer wg.Done()
			cancelErrs[idx] = s.CancelShipment(context.Background(), id)
		}(i, orderID)
	}
	wg.Wait()

	for i := range goroutines {
		require.NoErrorf(t, createErrs[i], "CreateShipment[%d]", i)
		require.NoErrorf(t, cancelErrs[i], "CancelShipment[%d]", i)
	}
}
