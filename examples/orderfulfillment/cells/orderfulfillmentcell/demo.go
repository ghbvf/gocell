package orderfulfillmentcell

import (
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/ports"
	sagaimpl "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/saga"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
)

// DemoStores bundles the in-memory store implementations for demo / local
// development mode. The composition root constructs a DemoStores value,
// injects the same instances into both the cell and the saga implementation,
// so that orders created by the HTTP handler are visible to the saga steps.
type DemoStores struct {
	Orders    *mem.OrderRepository
	Inventory *mem.InventoryStore
	Payments  *mem.PaymentStore
	Shipments *mem.ShipmentStore
}

// NewDemoStores constructs a DemoStores with pre-seeded inventory.
// stock maps item name → initial available count.
func NewDemoStores(stock map[string]int) DemoStores {
	return DemoStores{
		Orders:    mem.NewOrderRepository(),
		Inventory: mem.NewInventoryStore(stock),
		Payments:  mem.NewPaymentStore(),
		Shipments: mem.NewShipmentStore(),
	}
}

// OrderRepository returns the demo order repository as a ports.OrderRepository.
func (d DemoStores) OrderRepository() ports.OrderRepository { return d.Orders }

// InventoryStore returns the demo inventory store as a ports.InventoryStore.
func (d DemoStores) InventoryStore() ports.InventoryStore { return d.Inventory }

// PaymentStore returns the demo payment store as a ports.PaymentStore.
func (d DemoStores) PaymentStore() ports.PaymentStore { return d.Payments }

// ShipmentStore returns the demo shipment store as a ports.ShipmentStore.
func (d DemoStores) ShipmentStore() ports.ShipmentStore { return d.Shipments }

// NewSagaImpl constructs the saga business logic implementation wired to the
// given demo stores. The returned value implements of.Impl and is ready to
// pass to of.Register.
func NewSagaImpl(d DemoStores) of.Impl {
	return sagaimpl.NewImpl(d.Orders, d.Inventory, d.Payments, d.Shipments)
}
