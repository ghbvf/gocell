package mem_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestOrderRepository(t *testing.T) {
	t.Parallel()

	t.Run("create_and_get", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		order := &domain.Order{
			ID:          "ord-1",
			Item:        "widget",
			AmountCents: 1000,
			CreatedAt:   time.Now(),
		}
		if err := r.Create(context.Background(), order); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := r.GetByID(context.Background(), "ord-1")
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if got.ID != order.ID || got.Item != order.Item || got.AmountCents != order.AmountCents {
			t.Errorf("got %+v, want %+v", got, order)
		}
	})

	t.Run("get_not_found", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		_, err := r.GetByID(context.Background(), "missing")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		var ec *errcode.Error
		if !errors.As(err, &ec) || ec.Kind != errcode.KindNotFound {
			t.Errorf("expected KindNotFound, got %v", err)
		}
	})

	t.Run("create_returns_copy", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		order := &domain.Order{ID: "ord-2", Item: "gadget", AmountCents: 500, CreatedAt: time.Now()}
		if err := r.Create(context.Background(), order); err != nil {
			t.Fatalf("Create: %v", err)
		}
		// mutate original; stored copy must be unaffected
		order.AmountCents = 99999
		got, _ := r.GetByID(context.Background(), "ord-2")
		if got.AmountCents == 99999 {
			t.Error("Create stored a reference, not a copy")
		}
	})

	t.Run("create_empty_id_returns_error", func(t *testing.T) {
		t.Parallel()
		r := mem.NewOrderRepository()
		order := &domain.Order{ID: "", Item: "widget", AmountCents: 100, CreatedAt: time.Now()}
		err := r.Create(context.Background(), order)
		if err == nil {
			t.Fatal("expected error for empty ID, got nil")
		}
		var ec *errcode.Error
		if !errors.As(err, &ec) || ec.Kind != errcode.KindInvalid {
			t.Errorf("expected KindInvalid, got %v", err)
		}
	})
}

func TestInventoryStore(t *testing.T) {
	t.Parallel()

	t.Run("reserve_and_release", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{"widget": 2})

		resID, err := s.Reserve(context.Background(), "ord-1", "widget")
		if err != nil {
			t.Fatalf("Reserve: %v", err)
		}
		if resID != "res-ord-1" {
			t.Errorf("reservationID = %q, want %q", resID, "res-ord-1")
		}
		if s.Available("widget") != 1 {
			t.Errorf("available after reserve = %d, want 1", s.Available("widget"))
		}

		if err := s.Release(context.Background(), "ord-1"); err != nil {
			t.Fatalf("Release: %v", err)
		}
		if s.Available("widget") != 2 {
			t.Errorf("available after release = %d, want 2", s.Available("widget"))
		}
	})

	t.Run("release_idempotent", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{"widget": 1})
		// release unknown order: must return nil
		if err := s.Release(context.Background(), "unknown"); err != nil {
			t.Fatalf("Release unknown: %v", err)
		}
		// double release: must return nil
		_, _ = s.Reserve(context.Background(), "ord-1", "widget")
		_ = s.Release(context.Background(), "ord-1")
		if err := s.Release(context.Background(), "ord-1"); err != nil {
			t.Fatalf("double Release: %v", err)
		}
	})

	t.Run("reserve_out_of_stock", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{"widget": 0})
		_, err := s.Reserve(context.Background(), "ord-1", "widget")
		if err == nil {
			t.Fatal("expected error for out-of-stock, got nil")
		}
	})

	t.Run("reserve_unknown_item", func(t *testing.T) {
		t.Parallel()
		s := mem.NewInventoryStore(map[string]int{})
		_, err := s.Reserve(context.Background(), "ord-1", "nonexistent")
		if err == nil {
			t.Fatal("expected error for unknown item, got nil")
		}
	})
}

func TestPaymentStore(t *testing.T) {
	t.Parallel()

	t.Run("charge_and_get", func(t *testing.T) {
		t.Parallel()
		s := mem.NewPaymentStore()
		payID, err := s.Charge(context.Background(), "ord-1", 1500)
		if err != nil {
			t.Fatalf("Charge: %v", err)
		}
		if payID != "pay-ord-1" {
			t.Errorf("paymentID = %q, want %q", payID, "pay-ord-1")
		}
		got, ok := s.Get("ord-1")
		if !ok || got != "pay-ord-1" {
			t.Errorf("Get = %q %v, want pay-ord-1 true", got, ok)
		}
	})

	t.Run("refund_removes_payment", func(t *testing.T) {
		t.Parallel()
		s := mem.NewPaymentStore()
		_, _ = s.Charge(context.Background(), "ord-1", 1000)
		if err := s.Refund(context.Background(), "ord-1"); err != nil {
			t.Fatalf("Refund: %v", err)
		}
		_, ok := s.Get("ord-1")
		if ok {
			t.Error("payment still present after refund")
		}
	})

	t.Run("refund_idempotent", func(t *testing.T) {
		t.Parallel()
		s := mem.NewPaymentStore()
		// refund unknown: must return nil
		if err := s.Refund(context.Background(), "unknown"); err != nil {
			t.Fatalf("Refund unknown: %v", err)
		}
		// double refund
		_, _ = s.Charge(context.Background(), "ord-2", 200)
		_ = s.Refund(context.Background(), "ord-2")
		if err := s.Refund(context.Background(), "ord-2"); err != nil {
			t.Fatalf("double Refund: %v", err)
		}
	})
}

func TestShipmentStore(t *testing.T) {
	t.Parallel()

	t.Run("create_and_get", func(t *testing.T) {
		t.Parallel()
		s := mem.NewShipmentStore()
		shipID, err := s.CreateShipment(context.Background(), "ord-1")
		if err != nil {
			t.Fatalf("CreateShipment: %v", err)
		}
		if shipID != "ship-ord-1" {
			t.Errorf("shipmentID = %q, want %q", shipID, "ship-ord-1")
		}
		got, ok := s.Get("ord-1")
		if !ok || got != "ship-ord-1" {
			t.Errorf("Get = %q %v, want ship-ord-1 true", got, ok)
		}
	})

	t.Run("cancel_removes_shipment", func(t *testing.T) {
		t.Parallel()
		s := mem.NewShipmentStore()
		_, _ = s.CreateShipment(context.Background(), "ord-1")
		if err := s.CancelShipment(context.Background(), "ord-1"); err != nil {
			t.Fatalf("CancelShipment: %v", err)
		}
		_, ok := s.Get("ord-1")
		if ok {
			t.Error("shipment still present after cancel")
		}
	})

	t.Run("cancel_idempotent", func(t *testing.T) {
		t.Parallel()
		s := mem.NewShipmentStore()
		// cancel unknown: must return nil
		if err := s.CancelShipment(context.Background(), "unknown"); err != nil {
			t.Fatalf("CancelShipment unknown: %v", err)
		}
		// double cancel
		_, _ = s.CreateShipment(context.Background(), "ord-2")
		_ = s.CancelShipment(context.Background(), "ord-2")
		if err := s.CancelShipment(context.Background(), "ord-2"); err != nil {
			t.Fatalf("double CancelShipment: %v", err)
		}
	})
}

// TestInventoryStore_ConcurrentReserveRelease verifies that concurrent
// Reserve/Release operations on InventoryStore are data-race-free.
// Run with -race to detect any missing mutex guards.
func TestInventoryStore_ConcurrentReserveRelease(t *testing.T) {
	t.Parallel()
	const goroutines = 20
	s := mem.NewInventoryStore(map[string]int{"widget": goroutines})

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := range goroutines {
		orderID := "ord-" + string(rune('A'+i))
		go func(id string) {
			defer wg.Done()
			_, _ = s.Reserve(context.Background(), id, "widget")
		}(orderID)
		go func(id string) {
			defer wg.Done()
			_ = s.Release(context.Background(), id)
		}(orderID)
	}
	wg.Wait()
	// Available() must also be race-free.
	_ = s.Available("widget")
}

// TestPaymentStore_ConcurrentChargeRefund verifies that concurrent
// Charge/Refund operations on PaymentStore are data-race-free.
func TestPaymentStore_ConcurrentChargeRefund(t *testing.T) {
	t.Parallel()
	const goroutines = 20
	s := mem.NewPaymentStore()

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := range goroutines {
		orderID := "ord-" + string(rune('A'+i))
		go func(id string) {
			defer wg.Done()
			_, _ = s.Charge(context.Background(), id, 100)
		}(orderID)
		go func(id string) {
			defer wg.Done()
			_ = s.Refund(context.Background(), id)
		}(orderID)
	}
	wg.Wait()
	// Get must also be race-free.
	_, _ = s.Get("ord-A")
}

// TestShipmentStore_ConcurrentCreateCancel verifies that concurrent
// CreateShipment/CancelShipment operations on ShipmentStore are data-race-free.
func TestShipmentStore_ConcurrentCreateCancel(t *testing.T) {
	t.Parallel()
	const goroutines = 20
	s := mem.NewShipmentStore()

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := range goroutines {
		orderID := "ord-" + string(rune('A'+i))
		go func(id string) {
			defer wg.Done()
			_, _ = s.CreateShipment(context.Background(), id)
		}(orderID)
		go func(id string) {
			defer wg.Done()
			_ = s.CancelShipment(context.Background(), id)
		}(orderID)
	}
	wg.Wait()
	// Get must also be race-free.
	_, _ = s.Get("ord-A")
}
