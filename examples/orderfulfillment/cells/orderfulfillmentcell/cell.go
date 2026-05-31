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
	placeorderslice "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell/slices/placeorder"
	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/saga/journal"
)

// Option configures an OrderCell.
type Option func(*OrderCell)

// WithJournal injects the saga journal used for instance enrollment.
// A nil value is a silent noop.
func WithJournal(j journal.JournalCore) Option {
	return func(c *OrderCell) {
		if j != nil {
			c.journal = j
		}
	}
}

// WithOrderRepo injects the order repository.
// A nil value is a silent noop.
func WithOrderRepo(r ports.OrderRepository) Option {
	return func(c *OrderCell) {
		if r != nil {
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
// A nil value is a silent noop.
func WithCoordinator(p healthz.RepoProber) Option {
	return func(c *OrderCell) {
		if p != nil {
			c.coord = p
		}
	}
}

// OrderCell is the orderfulfillmentcell Cell implementation.
type OrderCell struct {
	*cell.BaseCell

	journal journal.JournalCore
	repo    ports.OrderRepository
	logger  *slog.Logger

	// coord is the optional saga Coordinator injected via WithCoordinator.
	// When non-nil it is registered with /readyz via RegisterReadiness so
	// operators can observe coordinator liveness.
	coord healthz.RepoProber

	// placeorderHandler is built in initInternal and mounted by the RouteGroup
	// declared in initInternal.
	placeorderHandler *placeordergen.Handler

	// placeorderSvc holds the slice service for slice metadata wiring.
	placeorderSvc *placeorderslice.Service
}

// NewOrderCell creates a new OrderCell with the given options.
func NewOrderCell(opts ...Option) *OrderCell {
	c := &OrderCell{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (slice service construction, route mounting).
// cell_gen.go::Init calls it after BaseCell.Init.
//
//nolint:unparam // ctx is a contract parameter; unused here, used by other cells
func (c *OrderCell) initInternal(ctx context.Context, reg cell.Registrar) error {
	// Register saga coordinator readiness probe when a coordinator was injected.
	// This makes /readyz reflect coordinator health (journal reachable + tick loop alive).
	if c.coord != nil {
		if err := RegisterReadiness(reg, c.coord); err != nil {
			return fmt.Errorf("orderfulfillmentcell: register coordinator readiness: %w", err)
		}
	}

	// Default to in-memory repository if none injected.
	if c.repo == nil {
		c.repo = mem.NewOrderRepository()
		c.logger.Info("orderfulfillmentcell: using in-memory order repository (demo mode)")
	}

	// Build the placeorder service.
	// The journal must be injected via WithJournal by the composition root.
	svc, err := placeorderslice.NewService(clock.Real(),
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

	reg.RouteGroup(cell.RouteGroup{
		Listener: cell.PrimaryListener,
		Prefix:   "/api/v1",
		Register: func(mux cell.RouteMux) error {
			var firstErr error
			captureErr := func(err error) {
				if err != nil && firstErr == nil {
					firstErr = err
				}
			}
			mux.Route("/orders", func(s cell.RouteMux) {
				captureErr(c.placeorderHandler.RegisterRoutes(s))
			})
			return firstErr
		},
	})

	return nil
}
