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
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(c *OrderCell) { c.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees.
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *OrderCell) { c.txRunner = tx }
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
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
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

	if (c.outboxWriter == nil) != (c.txRunner == nil) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"order-cell durable mode requires both outboxWriter and txRunner")
	}
	if (c.outboxWriter != nil || c.txRunner != nil) && c.repo == nil {
		return errcode.New(errcode.ErrValidationFailed,
			"order-cell durable mode requires explicit repository injection")
	}

	// Default to in-memory repository if none injected.
	if c.repo == nil {
		c.repo = mem.NewOrderRepository()
		c.logger.Info("order-cell: using in-memory repository (demo mode)")
	}

	if c.publisher == nil && c.outboxWriter == nil {
		c.logger.Warn("order-cell: no publisher injected, direct publish path disabled in demo mode")
	}

	// order-create slice
	var createOpts []ordercreate.Option
	if c.outboxWriter != nil {
		createOpts = append(createOpts, ordercreate.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		createOpts = append(createOpts, ordercreate.WithTxManager(c.txRunner))
	}
	createSvc := ordercreate.NewService(c.repo, c.publisher, c.logger, createOpts...)
	c.createHandler = ordercreate.NewHandler(createSvc)
	c.AddSlice(cell.NewBaseSlice("order-create", "order-cell", cell.L2))

	// Default cursor codec for pagination if not injected.
	if c.cursorCodec == nil {
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-ORDER-CELL-key-32b!!"))
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
