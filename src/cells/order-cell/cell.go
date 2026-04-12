// Package ordercell implements the order-cell Cell for the todo-order example.
// It demonstrates the "golden path" of building a business Cell with HTTP
// endpoints and event publishing using the GoCell framework.
package ordercell

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/order-cell/internal/mem"
	ordercreate "github.com/ghbvf/gocell/cells/order-cell/slices/order-create"
	orderquery "github.com/ghbvf/gocell/cells/order-cell/slices/order-query"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time interface checks.
var (
	_ cell.Cell          = (*OrderCell)(nil)
	_ cell.HTTPRegistrar = (*OrderCell)(nil)
)

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(c *query.CursorCodec) Option {
	return func(oc *OrderCell) { oc.cursorCodec = c }
}

// Option configures an OrderCell.
type Option func(*OrderCell)

// WithRepository sets the order repository.
func WithRepository(r domain.OrderRepository) Option {
	return func(c *OrderCell) { c.repo = r }
}

// WithPublisher sets the outbox Publisher for event publishing.
func WithPublisher(p outbox.Publisher) Option {
	return func(c *OrderCell) { c.publisher = p }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *OrderCell) { c.logger = l }
}

// OrderCell is the order-cell Cell implementation.
type OrderCell struct {
	*cell.BaseCell
	repo        domain.OrderRepository
	publisher   outbox.Publisher
	cursorCodec *query.CursorCodec
	logger      *slog.Logger

	createHandler *ordercreate.Handler
	queryHandler  *orderquery.Handler
}

// NewOrderCell creates a new OrderCell with the given options.
func NewOrderCell(opts ...Option) *OrderCell {
	c := &OrderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "order-cell",
			Type:             cell.CellTypeCore,
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "examples", Role: "order-owner"},
			Schema:           cell.SchemaConfig{Primary: "orders"},
			Verify:           cell.CellVerify{Smoke: []string{"order-cell/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Init sets up repositories, slice services, and handlers.
// In demo mode, missing dependencies are replaced with in-memory defaults.
func (c *OrderCell) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Default to in-memory repository if none injected.
	if c.repo == nil {
		c.repo = mem.NewOrderRepository()
		c.logger.Info("order-cell: using in-memory repository (demo mode)")
	}

	// L2 Cell would normally require outboxWriter; in demo mode we skip that
	// and use the publisher directly for event publishing.
	if c.publisher == nil {
		c.logger.Warn("order-cell: no publisher injected, events will not be published")
	}

	// order-create slice
	createSvc := ordercreate.NewService(c.repo, c.publisher, c.logger)
	c.createHandler = ordercreate.NewHandler(createSvc)
	c.AddSlice(cell.NewBaseSlice("order-create", "order-cell", cell.L2))

	// Default cursor codec for pagination if not injected.
	if c.cursorCodec == nil {
		// Demo mode: use a deterministic key. Production should inject via WithCursorCodec.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("order-cell: using default cursor codec (demo mode)")
	}

	// order-query slice
	querySvc := orderquery.NewService(c.repo, c.cursorCodec, c.logger)
	c.queryHandler = orderquery.NewHandler(querySvc)
	c.AddSlice(cell.NewBaseSlice("order-query", "order-cell", cell.L0))

	return nil
}

// RegisterRoutes registers HTTP routes for order-cell.
func (c *OrderCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		v1.Route("/orders", func(orders cell.RouteMux) {
			orders.Handle("POST /", http.HandlerFunc(c.createHandler.HandleCreate))
			orders.Handle("GET /", http.HandlerFunc(c.queryHandler.HandleList))
			orders.Handle("GET /{id}", http.HandlerFunc(c.queryHandler.HandleGet))
		})
	})
}
