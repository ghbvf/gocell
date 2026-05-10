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

// WithOutboxDeps wires raw outbox dependencies (Publisher + Writer). The
// framework composes them into an outbox.Emitter at Init() time via
// cell.ResolveCellEmitter — the same path platform cells use.
//
// Accumulative: a nil argument leaves the previously-set value in place;
// multiple calls combine their non-nil arguments. Does NOT clear previous
// state — `WithOutboxDeps(nil, nil)` is a no-op, not a reset.
//
// Tests typically pass `(nil, outbox.NoopWriter{})` for a sink that swallows
// events without producing real fan-out.
//
// Demo mode: pass (nil, outbox.NoopWriter{}) — Publisher is unused when relay is absent;
// the writer is a non-fan-out sink that swallows events for local testing.
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D6
// ref: cells/auditcore/cell.go::WithOutboxDeps (platform-cell pattern)
func WithOutboxDeps(pub outbox.Publisher, writer outbox.Writer) Option {
	return func(c *OrderCell) {
		if pub != nil {
			c.pendingOutboxPub = pub
		}
		if writer != nil {
			c.pendingOutboxWriter = writer
		}
	}
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
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
type OrderCell struct {
	*cell.BaseCell
	repo     domain.OrderRepository
	txRunner persistence.TxRunner
	emitter  outbox.Emitter
	// Outbox wiring — raw deps are accumulated via WithOutboxDeps and
	// composed into emitter at Init() via cell.ResolveCellEmitter; archtest
	// CELL-RAW-DEPS-01 forbids exporting raw outbox.Publisher/Writer Options.
	pendingOutboxPub    outbox.Publisher
	pendingOutboxWriter outbox.Writer
	cursorCodec         *query.CursorCodec
	logger              *slog.Logger

	// +slice:route:slice=ordercreate,subPath=/orders
	createHandler *createv1.Handler

	// +slice:route:slice=orderquery,subPath=/orders
	getHandler *getv1.Handler

	// +slice:route:slice=orderquery,subPath=/orders
	listHandler *listv1.Handler
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
// be generated (emitter resolve, slice service construction, codec defaults).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated route-group blocks. This is a permanent convention, not a
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

	// Register emitter health probes (fail-open rate checker), aligning with
	// platform cell pattern (auditcore/cell.go:220-224, configcore/cell.go).
	if hc, ok := c.emitter.(cell.HealthProber); ok {
		for k, v := range hc.Probes() {
			reg.Health(k, v)
		}
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
			return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
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

// resolveOutboxDeps delegates to cell.ResolveCellEmitter — the same path
// platform cells (accesscore/auditcore/configcore) use. After this call,
// pendingOutboxPub/pendingOutboxWriter are cleared and c.emitter is the
// composed sink.
//
// ordercell uses the framework default (DirectPublishMode = zero value;
// publisher path requires MetricsProvider which ordercell does not wire,
// so pendingOutboxPub == nil in current usage). The (pub, writer) pair
// keeps the door open without committing ordercell to publisher semantics.
func (c *OrderCell) resolveOutboxDeps(mode cell.DurabilityMode) error {
	outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
		EmitterConfig: cell.EmitterConfig{
			CellID:       "ordercell",
			Mode:         mode,
			Publisher:    c.pendingOutboxPub,
			OutboxWriter: c.pendingOutboxWriter,
			TxRunner:     c.txRunner,
			Logger:       c.logger,
		},
		PreResolved:      c.emitter,
		ConsistencyLevel: c.ConsistencyLevel(),
	})
	if err != nil {
		return err
	}
	c.emitter = outcome.Emitter
	c.pendingOutboxPub = nil
	c.pendingOutboxWriter = nil
	return nil
}
