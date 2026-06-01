// Package orderfulfillmentcell implements the orderfulfillmentcell Cell for the
// orderfulfillment example. It demonstrates an L3 WorkflowEventual cell that
// uses the saga engine for fulfillment orchestration.
//
// The cell has no outbox publisher or transactional writer — saga coordination
// is handled by the Coordinator wired in the composition root (run.go).
// The placeorder slice enrolls saga instances via the shared MemJournal.
package orderfulfillmentcell

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/mem"
	"github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/internal/ports"
	orderstatusslice "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/orderstatus"
	placeorderslice "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Option configures an OrderCell.
type Option func(*OrderCell)

// WithJournal injects the saga journal used for instance enrollment and status
// queries. Typed-nil inputs are not stored; the cell initInternal will
// fail-fast if journal remains unset.
//
// The cell holds the narrow ProducerReader interface rather than JournalCore,
// so business code cannot access coordinator-only methods (ClaimPending,
// Append, MarkTerminal). The placeorder slice receives the Enqueuer subset and
// the orderstatus slice receives the Reader subset — both satisfied by any
// ProducerReader value.
func WithJournal(j journal.ProducerReader) Option {
	return func(c *OrderCell) {
		if !validation.IsNilInterface(j) {
			c.journal = j
		}
	}
}

// WithOrderRepo injects the order repository.
// Typed-nil inputs are not stored; initInternal falls back to in-memory repo.
func WithOrderRepo(r ports.OrderRepository) Option {
	return func(c *OrderCell) {
		if !validation.IsNilInterface(r) {
			c.repo = r
		}
	}
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *OrderCell) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithCoordinator injects the saga Coordinator as a healthz.RepoProber so
// the cell can register it with /readyz. The coordinator's RepoReady probe
// checks that the journal is reachable and the tick loop is healthy.
// Typed-nil inputs are not stored; initInternal will fail-fast if coord remains unset.
func WithCoordinator(p healthz.RepoProber) Option {
	return func(c *OrderCell) {
		if !validation.IsNilInterface(p) {
			c.coord = p
		}
	}
}

// OrderCell is the orderfulfillmentcell Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type OrderCell struct {
	*cell.BaseCell

	clock   clock.Clock
	journal journal.ProducerReader
	repo    ports.OrderRepository
	logger  *slog.Logger

	// coord is the optional saga Coordinator injected via WithCoordinator.
	// When non-nil it is registered with /readyz via RegisterReadiness so
	// operators can observe coordinator liveness.
	coord healthz.RepoProber

	// placeorderHandler is built in initInternal and mounted on the
	// PrimaryListener by the cellgen-generated RouteGroup in cell_gen.go,
	// driven by the +slice:route marker below.
	// +slice:route:slice=placeorder,subPath=/orders
	placeorderHandler *placeordergen.Handler

	// placeorderSvc holds the slice service for slice metadata wiring.
	placeorderSvc *placeorderslice.Service

	// orderstatusHandler is built in initInternal and mounted on the
	// PrimaryListener by the cellgen-generated RouteGroup in cell_gen.go,
	// driven by the +slice:route marker below.
	// +slice:route:slice=orderstatus,subPath=/orders
	orderstatusHandler *orderstatusgen.Handler

	// orderstatusSvc holds the orderstatus slice service for slice metadata wiring.
	orderstatusSvc *orderstatusslice.Service
}

// NewOrderCell creates a new OrderCell with the given clock and options.
// clk is a mandatory positional parameter; clock.MustHaveClock panics on nil
// (programmer error — misuse at composition root, not a runtime condition).
func NewOrderCell(clk clock.Clock, opts ...Option) *OrderCell {
	clock.MustHaveClock(clk, "orderfulfillmentcell.NewOrderCell")
	c := &OrderCell{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		clock:    clk,
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (coordinator readiness registration, slice service
// construction, repo defaults). Route mounting is generated into
// cell_gen.go from the +slice:route markers on the handler fields.
// cell_gen.go::Init calls it after BaseCell.Init and before the generated
// RouteGroup blocks.
//
//nolint:unparam // ctx is a contract parameter; unused here, used by other cells
func (c *OrderCell) initInternal(ctx context.Context, reg cell.Registrar) error {
	// Coordinator is required: a cell that can accept orders must have a
	// coordinator readiness probe so /readyz reflects coordinator liveness.
	// Medium guard — hand-written validation.IsNilInterface; Hard path = gocell:"required"
	// tag funnel (currently covers Service structs, not cell/coordinator wiring),
	// tracked in gh #1317.
	if validation.IsNilInterface(c.coord) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"orderfulfillmentcell: saga coordinator required; inject via WithCoordinator")
	}
	if err := RegisterReadiness(reg, c.coord); err != nil {
		return fmt.Errorf("orderfulfillmentcell: register coordinator readiness: %w", err)
	}

	// Default to in-memory repository if none injected.
	if c.repo == nil {
		c.repo = mem.NewOrderRepository()
		c.logger.Info("orderfulfillmentcell: using in-memory order repository (demo mode)")
	}

	// Build the placeorder service.
	// The journal must be injected via WithJournal by the composition root.
	svc, err := placeorderslice.NewService(
		c.clock,
		placeorderslice.WithOrderRepository(c.repo),
		placeorderslice.WithJournal(c.journal),
		placeorderslice.WithLogger(c.logger),
	)
	if err != nil {
		return fmt.Errorf("orderfulfillmentcell: placeorder service: %w", err)
	}
	c.placeorderSvc = svc
	c.placeorderHandler = placeordergen.NewHandler(placeorderslice.NewHandler(svc))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(placeorderslice.SliceMetadata()))

	// Build the orderstatus service using the shared repo and journal.
	statusSvc, err := orderstatusslice.NewService(
		c.clock,
		orderstatusslice.WithOrderRepository(c.repo),
		orderstatusslice.WithJournal(c.journal),
		orderstatusslice.WithLogger(c.logger),
	)
	if err != nil {
		return fmt.Errorf("orderfulfillmentcell: orderstatus service: %w", err)
	}
	c.orderstatusSvc = statusSvc
	c.orderstatusHandler = orderstatusgen.NewHandler(orderstatusslice.NewHandler(statusSvc))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(orderstatusslice.SliceMetadata()))

	return nil
}
