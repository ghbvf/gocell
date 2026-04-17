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
	repo         domain.OrderRepository
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	cursorCodec  *query.CursorCodec
	logger       *slog.Logger

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
// outboxWriter and txRunner are required. For demo mode, inject
// outbox.NoopWriter{} + persistence.NoopTxRunner{} for a unified code path.
func (c *OrderCell) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// outboxWriter + txRunner are mandatory (demo uses NoopWriter + NoopTxRunner).
	if c.outboxWriter == nil || c.txRunner == nil {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"order-cell requires outboxWriter and txRunner; use outbox.NoopWriter{} + persistence.NoopTxRunner{} for demo mode")
	}

	// Durable mode: reject noop implementations (#27c-2 BOOTSTRAP-STRICT-MODE).
	if err := cell.CheckNotNoop(deps.DurabilityMode, "order-cell", c.outboxWriter, c.txRunner); err != nil {
		return err
	}

	// Default to in-memory repository if none injected.
	if c.repo == nil {
		c.repo = mem.NewOrderRepository()
		c.logger.Info("order-cell: using in-memory repository (demo mode)")
	}

	// order-create slice — unified outbox path, no publisher fork.
	createSvc := ordercreate.NewService(c.repo, c.logger,
		ordercreate.WithOutboxWriter(c.outboxWriter),
		ordercreate.WithTxManager(c.txRunner),
	)
	c.createHandler = ordercreate.NewHandler(createSvc)
	c.AddSlice(cell.NewBaseSlice("order-create", "order-cell", cell.L2))

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if deps.DurabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.ErrCellMissingCodec,
				"order-cell durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-ORDER-CELL-key-32b!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("order-cell: using default cursor codec (demo mode)")
	}

	// order-query slice
	querySvc := orderquery.NewService(c.repo, c.cursorCodec, c.logger,
		query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo))
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
