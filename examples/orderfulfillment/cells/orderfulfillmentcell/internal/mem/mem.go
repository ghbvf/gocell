// Package mem provides in-memory implementations of the ports interfaces.
// These implementations are suitable for demo mode and unit tests.
package mem

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// OrderRepository is an in-memory implementation of ports.OrderRepository.
type OrderRepository struct {
	mu     sync.Mutex
	orders map[string]*domain.Order
}

// NewOrderRepository returns a new empty in-memory OrderRepository.
func NewOrderRepository() *OrderRepository {
	return &OrderRepository{orders: make(map[string]*domain.Order)}
}

// Create stores the order. Returns ErrValidationFailed if the ID is empty,
// matching the godoc contract that requires a non-empty order.ID.
func (r *OrderRepository) Create(_ context.Context, order *domain.Order) error {
	if order.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"mem.OrderRepository.Create: order ID must not be empty")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := *order
	r.orders[order.ID] = &cp
	return nil
}

// GetByID retrieves an order by ID, returning KindNotFound / ErrOrderNotFound
// when the order does not exist.
func (r *OrderRepository) GetByID(_ context.Context, id string) (*domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrOrderNotFound, "order not found")
	}
	cp := *o
	return &cp, nil
}

// InventoryStore is an in-memory implementation of ports.InventoryStore.
type InventoryStore struct {
	mu           sync.Mutex
	available    map[string]int    // item → available count
	reservations map[string]string // orderID → item
}

// NewInventoryStore returns an in-memory InventoryStore pre-seeded with the
// given initial stock counts.
func NewInventoryStore(initial map[string]int) *InventoryStore {
	avail := make(map[string]int, len(initial))
	for k, v := range initial {
		avail[k] = v
	}
	return &InventoryStore{
		available:    avail,
		reservations: make(map[string]string),
	}
}

// Reserve decrements the available count for item and records the reservation.
// Returns errcode.KindConflict / ErrConflict when the item is unknown or has
// no stock.
func (s *InventoryStore) Reserve(_ context.Context, orderID, item string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count, ok := s.available[item]
	if !ok || count == 0 {
		return "", errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"inventory: item unavailable or out of stock")
	}
	s.available[item] = count - 1
	s.reservations[orderID] = item
	return fmt.Sprintf("res-%s", orderID), nil
}

// Release re-increments the available count and removes the reservation.
// Idempotent: unknown orderID returns nil.
func (s *InventoryStore) Release(_ context.Context, orderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.reservations[orderID]
	if !ok {
		return nil
	}
	s.available[item]++
	delete(s.reservations, orderID)
	return nil
}

// Available returns the current available count for item.
func (s *InventoryStore) Available(item string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.available[item]
}

// PaymentStore is an in-memory implementation of ports.PaymentStore.
type PaymentStore struct {
	mu       sync.Mutex
	payments map[string]string // orderID → paymentID
}

// NewPaymentStore returns a new empty in-memory PaymentStore.
func NewPaymentStore() *PaymentStore {
	return &PaymentStore{payments: make(map[string]string)}
}

// Charge records a payment and returns a deterministic paymentID.
func (s *PaymentStore) Charge(_ context.Context, orderID string, _ int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("pay-%s", orderID)
	s.payments[orderID] = id
	return id, nil
}

// Refund removes the payment record for orderID.
// Idempotent: unknown orderID returns nil.
func (s *PaymentStore) Refund(_ context.Context, orderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.payments, orderID)
	return nil
}

// Get returns the paymentID for orderID if a charge exists.
func (s *PaymentStore) Get(orderID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.payments[orderID]
	return id, ok
}

// ShipmentStore is an in-memory implementation of ports.ShipmentStore.
type ShipmentStore struct {
	mu        sync.Mutex
	shipments map[string]string // orderID → shipmentID
}

// NewShipmentStore returns a new empty in-memory ShipmentStore.
func NewShipmentStore() *ShipmentStore {
	return &ShipmentStore{shipments: make(map[string]string)}
}

// CreateShipment records a shipment and returns a deterministic shipmentID.
func (s *ShipmentStore) CreateShipment(_ context.Context, orderID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := fmt.Sprintf("ship-%s", orderID)
	s.shipments[orderID] = id
	return id, nil
}

// CancelShipment removes the shipment record for orderID.
// Idempotent: unknown orderID returns nil.
func (s *ShipmentStore) CancelShipment(_ context.Context, orderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shipments, orderID)
	return nil
}

// Get returns the shipmentID for orderID if a shipment exists.
func (s *ShipmentStore) Get(orderID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.shipments[orderID]
	return id, ok
}
