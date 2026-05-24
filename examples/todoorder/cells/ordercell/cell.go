// Package ordercell implements the ordercell Cell for the todoorder example.
// It demonstrates the "golden path" of building a business Cell with HTTP
// endpoints and event publishing using the GoCell framework.
package ordercell

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	orderconfirm "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderconfirm"
	ordercreate "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/ordercreate"
	orderprojection "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderprojection"
	orderprojectionrebuild "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderprojectionrebuild"
	orderquery "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/slices/orderquery"
	confirmv1 "github.com/ghbvf/gocell/generated/contracts/http/order/confirm/v1"
	createv1 "github.com/ghbvf/gocell/generated/contracts/http/order/create/v1"
	getv1 "github.com/ghbvf/gocell/generated/contracts/http/order/get/v1"
	projectionrebuildv1 "github.com/ghbvf/gocell/generated/contracts/http/order/internalapi/projection-rebuild/v1"
	listv1 "github.com/ghbvf/gocell/generated/contracts/http/order/list/v1"
	projectionsummaryv1 "github.com/ghbvf/gocell/generated/contracts/http/order/projection-summary/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// allowAllInternalPolicy is the route policy for the internal projection-rebuild
// endpoint. The caller-cell allowlist guard is auto-injected by auth.Mount
// because http.order.internal.projection-rebuild.v1 declares non-empty endpoints.clients
// ([ordercell]); the service-token + caller-cell check happens at the transport
// layer, so this policy adds no JWT role check (internal requests carry a
// service principal, not a JWT role).
var allowAllInternalPolicy auth.Policy = func(*http.Request) error { return nil }

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

// WithOutboxWriter wires the sealed outbox CellWriter for transactional
// event publishing. ordercell is L2 OutboxFact — the (writer, txRunner)
// pair is composed into an outbox.Emitter at Init() time via
// outbox.ResolveCellEmitter. Composition roots construct via
// outbox.WrapWriterForCell.
//
// ordercell deliberately omits the publisher-only path: it has no
// MetricsProvider/Clock wiring for a DirectEmitter. The writer+txRunner
// sink is the only supported configuration.
//
// Accumulative: a nil writer leaves the previously-set value in place.
// Demo mode: wrap outbox.NoopWriter{} with outbox.WrapWriterForCell, paired
// with WithTxManager(persistence.WrapForCell(demoTxRunner{})) or
// outbox.DemoCellTxManager().
//
// AI-HARD per ADR cell-raw-infra-sealed-marker: the option signature
// rejects raw outbox.Writer at compile time.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
func WithOutboxWriter(writer outbox.CellWriter) Option {
	return func(c *OrderCell) {
		if writer != nil {
			c.pendingOutboxWriter = writer
		}
	}
}

// WithTxManager sets the CellTxManager for transactional guarantees.
// Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(c *OrderCell) { c.txRunner = tx }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *OrderCell) { c.logger = l }
}

// OrderCell is the ordercell Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
// +cell:listener:ref=cell.InternalListener,prefix=/internal/v1
type OrderCell struct {
	*cell.BaseCell
	repo     domain.OrderRepository
	txRunner persistence.CellTxManager
	emitter  outbox.Emitter
	// Outbox wiring — writer accumulated via WithOutboxWriter and composed into
	// emitter at Init() via outbox.ResolveCellEmitter. ordercell is L2 OutboxFact:
	// writer+txRunner is the only supported sink. Sealed marker types prevent
	// any cell.go public Option from accepting raw outbox.Writer at compile
	// time (ADR cell-raw-infra-sealed-marker §D1).
	pendingOutboxWriter outbox.CellWriter
	cursorCodec         *query.CursorCodec
	logger              *slog.Logger

	// +slice:route:slice=ordercreate,subPath=/orders
	createHandler *createv1.Handler

	// +slice:route:slice=orderquery,subPath=/orders
	getHandler *getv1.Handler

	// +slice:route:slice=orderquery,subPath=/orders
	listHandler *listv1.Handler

	// +slice:route:slice=orderconfirm,subPath=/orders
	confirmHandler *confirmv1.Handler

	// +slice:route:slice=orderprojection,subPath=/orders
	projectionSummaryHandler *projectionsummaryv1.Handler

	// +slice:route:slice=orderprojectionrebuild,listener=cell.InternalListener,subPath=/orders
	projectionRebuildHandler *projectionrebuildv1.Handler

	// +slice:subscribe:slice=orderprojection,topic=event.order-created.v1,handler=HandleOrderCreated,group=ordercell
	// +slice:subscribe:slice=orderprojection,topic=event.order-status-changed.v1,handler=HandleOrderStatusChanged,group=ordercell
	projectionSvc *orderprojection.Service
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
func (c *OrderCell) initInternal(ctx context.Context, reg cell.Registrar) error {
	durabilityMode := reg.DurabilityMode()

	if err := c.resolveOutboxDeps(durabilityMode); err != nil {
		return err
	}

	// Register emitter health probes (fail-open rate checker) via the
	// cellgen-generated typed funnel.
	if hc, ok := c.emitter.(healthz.ProbeSet); ok {
		if err := RegisterEmitterProbes(reg, hc); err != nil {
			return err
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
	c.AddSlice(cell.MustNewBaseSliceFromMeta(ordercreate.SliceMetadata()))

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if durabilityMode == outbox.DurabilityDurable {
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
		query.RunModeForDemo(durabilityMode == outbox.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("order-query: %w", err)
	}
	c.getHandler = getv1.NewHandler(querySvc, auth.AnyRole(dto.RoleCustomer))
	c.listHandler = listv1.NewHandler(querySvc, auth.AnyRole(dto.RoleCustomer))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(orderquery.SliceMetadata()))

	// order-confirm slice (L2 OutboxFact) — PATCH status to confirmed, publishes
	// event.order-status-changed.v1 through the same emitter+txRunner sink as
	// order-create.
	confirmSvc, err := orderconfirm.NewService(c.repo, c.logger,
		orderconfirm.WithEmitter(c.emitter),
		orderconfirm.WithTxManager(c.txRunner),
	)
	if err != nil {
		return fmt.Errorf("orderconfirm: %w", err)
	}
	c.confirmHandler = confirmv1.NewHandler(confirmSvc, auth.AnyRole(dto.RoleCustomer))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(orderconfirm.SliceMetadata()))

	// order-projection slice (L3 WorkflowEventual) — subscribes to order-created
	// and order-status-changed, maintains an in-memory status-grouped read model
	// (projection.order.status-summary.v1), serves the summary query, and exposes
	// a business-level rebuild on the internal listener. The subscription wiring
	// is emitted into cell_gen.go from the +slice:subscribe markers above.
	projSvc, err := orderprojection.NewService(orderprojection.WithLogger(c.logger))
	if err != nil {
		return fmt.Errorf("orderprojection: %w", err)
	}
	c.projectionSvc = projSvc
	c.projectionSummaryHandler = projectionsummaryv1.NewHandler(
		orderprojection.NewSummaryAdapter(projSvc), auth.AnyRole(dto.RoleCustomer))
	c.AddSlice(cell.MustNewBaseSliceFromMeta(orderprojection.SliceMetadata()))

	// order-projection-rebuild slice (internal control-plane) — POST
	// /internal/v1/orders/projection/rebuild, kept separate from the public
	// projection slice per FMT-33 (public/internal trust-boundary segregation).
	// It shares the projection store via the injected *orderprojection.Service.
	c.projectionRebuildHandler = projectionrebuildv1.NewHandler(
		orderprojectionrebuild.NewRebuildAdapter(projSvc), allowAllInternalPolicy)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(orderprojectionrebuild.SliceMetadata()))

	return nil
}

// resolveOutboxDeps delegates to outbox.ResolveCellEmitter — the same path
// platform cells (accesscore/auditcore/configcore) use. ordercell only
// supports the writer+txRunner sink (L2 OutboxFact).
// After this call, pendingOutboxWriter is cleared and c.emitter is the
// composed sink.
func (c *OrderCell) resolveOutboxDeps(mode outbox.DurabilityMode) error {
	outcome, err := outbox.ResolveCellEmitter(outbox.CellEmitterInputs{
		EmitterConfig: outbox.EmitterConfig{
			CellID:       "ordercell",
			Mode:         mode,
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
	c.pendingOutboxWriter = nil
	return nil
}
