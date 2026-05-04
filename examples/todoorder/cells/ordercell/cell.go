// Package ordercell implements the ordercell Cell for the todoorder example.
// It demonstrates the "golden path" of building a business Cell with HTTP
// endpoints and event publishing using the GoCell framework.
package ordercell

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	ordercreate "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/ordercreate"
	orderquery "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderquery"
	createv1 "github.com/ghbvf/gocell/generated/contracts/http/order/create/v1"
	getv1 "github.com/ghbvf/gocell/generated/contracts/http/order/get/v1"
	listv1 "github.com/ghbvf/gocell/generated/contracts/http/order/list/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Role constants re-exported from internal/dto for use by the assembly root
// (main.go). The internal package is not importable from outside the
// examples/todoorder/cells/ordercell subtree per Go's internal package rule.
const (
	RoleCustomer = dto.RoleCustomer
)

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

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

// OrderCell is the ordercell Cell implementation.
type OrderCell struct {
	*cell.BaseCell
	repo         domain.OrderRepository
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	emitter      outbox.Emitter
	cursorCodec  *query.CursorCodec
	logger       *slog.Logger

	createHandler *createv1.Handler
	getHandler    *getv1.Handler
	listHandler   *listv1.Handler
}

// NewOrderCell creates a new OrderCell with the given options.
func NewOrderCell(opts ...Option) *OrderCell {
	c := &OrderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "ordercell",
			Type:             cell.CellTypeCore,
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "examples", Role: "order-owner"},
			Schema:           cell.SchemaConfig{Primary: "orders"},
			Verify:           cell.CellVerify{Smoke: []string{"ordercell/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, codec defaults).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated reg.RouteGroup() blocks. This is a permanent convention, not a
// transitional shim — slice/handler instantiation and adapter wiring stay
// hand-written.
//
// ctx is part of the contract because cells that ping adapters (postgres,
// vault) at init time need it; ordercell currently does not.
//
//nolint:unparam // ctx is a contract parameter; unused here, used by other cells
func (c *OrderCell) initInternal(ctx context.Context, reg cell.Registry) error {
	durabilityMode := reg.DurabilityMode()

	if err := c.resolveOutboxDeps(durabilityMode); err != nil {
		return err
	}

	// Default to in-memory repository if none injected.
	if c.repo == nil {
		c.repo = mem.NewOrderRepository()
		c.logger.Info("ordercell: using in-memory repository (demo mode)")
	}

	// order-create slice — unified outbox path, no publisher fork.
	createSvc, err := ordercreate.NewService(c.repo, c.logger,
		ordercreate.WithEmitter(c.emitter),
		ordercreate.WithTxManager(c.txRunner),
	)
	if err != nil {
		return fmt.Errorf("ordercreate: %w", err)
	}
	c.createHandler = createv1.NewHandler(createSvc, auth.AnyRole(dto.RoleCustomer))
	c.AddSlice(cell.NewBaseSlice("ordercreate", "ordercell", cell.L2))

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if durabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.ErrCellMissingCodec,
				"ordercell durable mode requires a cursor codec; "+
					"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
					"the built-in demo key is public in the source tree")
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-ORDER-CELL-key-32b!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("ordercell: using default cursor codec (demo mode)")
	}

	// order-query slice
	querySvc, err := orderquery.NewService(c.repo, c.cursorCodec, c.logger,
		query.RunModeForDemo(durabilityMode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("order-query: %w", err)
	}
	c.getHandler = getv1.NewHandler(querySvc, auth.AnyRole(dto.RoleCustomer))
	c.listHandler = listv1.NewHandler(querySvc, auth.AnyRole(dto.RoleCustomer))
	c.AddSlice(cell.NewBaseSlice("orderquery", "ordercell", cell.L0))

	return nil
}

func (c *OrderCell) resolveOutboxDeps(mode cell.DurabilityMode) error {
	if err := cell.CheckNotNoop(mode, "ordercell", c.outboxWriter, c.txRunner); err != nil {
		return err
	}
	if mode == cell.DurabilityDurable {
		if c.outboxWriter == nil || c.txRunner == nil {
			return errcode.New(errcode.ErrCellMissingOutbox,
				"ordercell durable mode requires real outboxWriter and txRunner")
		}
		emitter, err := outbox.NewWriterEmitter(c.outboxWriter)
		if err != nil {
			return err
		}
		c.emitter = emitter
		return nil
	}
	if c.outboxWriter == nil || c.txRunner == nil {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"ordercell demo mode requires outboxWriter and txRunner together; inject both explicitly")
	}
	emitter, err := outbox.NewWriterEmitter(c.outboxWriter)
	if err != nil {
		return err
	}
	c.emitter = emitter
	return nil
}
