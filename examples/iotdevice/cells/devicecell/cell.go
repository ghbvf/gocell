// Package devicecell implements the devicecell Cell for the iotdevice example.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by devices on their own schedule.
package devicecell

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	devicecommand "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommand"
	devicelist "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicelist"
	deviceregister "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/deviceregister"
	devicestatus "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicestatus"
	ackcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/ack/v1"
	dequeuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/dequeue/v1"
	enqueuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue/v1"
	extendleasecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/extend-lease/v1"
	reportcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/report/v1"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/device/list/v1"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	statuscontract "github.com/ghbvf/gocell/generated/contracts/http/device/status/v1"
	internallistcontract "github.com/ghbvf/gocell/generated/contracts/http/internalapi/devicecommands/list/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	commandruntime "github.com/ghbvf/gocell/runtime/command"
)

// Role constants re-exported from internal/dto for use by the assembly root
// (main.go). The internal package is not importable from outside the
// examples/iotdevice/cells/devicecell subtree per Go's internal package rule.
const (
	RoleAdmin    = dto.RoleAdmin
	RoleOperator = dto.RoleOperator
	RoleDevice   = dto.RoleDevice
)

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

type commandQueueStore interface {
	kcommand.Queue
	kcommand.ActiveScanner
}

// Option configures a DeviceCell.
type Option func(*DeviceCell)

// WithDeviceRepository sets the device repository.
func WithDeviceRepository(r domain.DeviceRepository) Option {
	return func(c *DeviceCell) { c.deviceRepo = r }
}

// WithDirectPublisher wires the sealed outbox CellPublisher for event publishing.
// devicecell is L4 DeviceLatent — the direct-publish path is the source
// of truth. There is no transactional outbox writer at L4.
//
// Accumulative: a nil pub leaves the previously-set value in place.
// Demo mode: from the composition root, pass
// outbox.WrapPublisherForCell(&outbox.DiscardPublisher{}) to swallow events.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
func WithDirectPublisher(pub outbox.CellPublisher) Option {
	return func(c *DeviceCell) {
		if pub != nil {
			c.publisher = pub
		}
	}
}

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(c *query.CursorCodec) Option {
	return func(dc *DeviceCell) { dc.cursorCodec = c }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *DeviceCell) { c.logger = l }
}

// WithMetricsProvider sets the metrics.Provider used by the outbox emitter to
// record fail-open dropped counters. Defaults to metrics.NopProvider{} when not
// set (appropriate for demo/example deployments).
func WithMetricsProvider(mp metrics.Provider) Option {
	return func(c *DeviceCell) { c.metricsProvider = mp }
}

// WithClock sets the clock used by this cell. Must be called before Init.
func WithClock(clk clock.Clock) Option {
	return func(c *DeviceCell) { c.clk = clk }
}

// DeviceCell is the devicecell Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=
// +cell:listener:ref=cell.InternalListener,prefix=
type DeviceCell struct {
	*cell.BaseCell
	deviceRepo      domain.DeviceRepository
	publisher       outbox.CellPublisher
	emitter         outbox.Emitter // set during initInternal; retained for Probes
	cursorCodec     *query.CursorCodec
	logger          *slog.Logger
	metricsProvider metrics.Provider
	commandQueue    commandQueueStore
	commandSweeper  *commandruntime.SweeperLifecycle
	clk             clock.Clock // injected from reg.Config during initInternal

	// +slice:route:slice=deviceregister,subPath=/api/v1/devices
	registerHandler *registercontract.Handler

	// +slice:route:slice=devicecommand,subPath=/api/v1/devices
	commandEnqueueHandler *enqueuecontract.Handler
	// +slice:route:slice=devicecommand,subPath=/api/v1/devices
	commandDequeueHandler *dequeuecontract.Handler
	// +slice:route:slice=devicecommand,subPath=/api/v1/devices
	commandReportHandler *reportcontract.Handler
	// +slice:route:slice=devicecommand,subPath=/api/v1/devices
	commandAckHandler *ackcontract.Handler
	// +slice:route:slice=devicecommand,subPath=/api/v1/devices
	commandExtendLeaseHandler *extendleasecontract.Handler
	// +slice:route:slice=devicecommand,listener=cell.InternalListener,subPath=
	commandInternalHandler *internallistcontract.Handler

	// +slice:route:slice=devicestatus,subPath=/api/v1/devices
	statusHandler *statuscontract.Handler

	// +slice:route:slice=devicelist,subPath=/api/v1/devices
	listHandler *listcontract.Handler
}

// RegisterCommandQueue implements kernel/command.QueueRegistrar. The supplied
// queue must also implement ActiveScanner so the same runtime component can
// serve the device dequeue path, sweeper, and internal ops view.
func (c *DeviceCell) RegisterCommandQueue(q kcommand.Queue) {
	store, ok := q.(commandQueueStore)
	if !ok {
		c.logger.Warn("devicecell: command queue does not implement ActiveScanner; ignoring registrar injection")
		return
	}
	c.commandQueue = store
}

// NewDeviceCell creates a new DeviceCell with the given options.
func NewDeviceCell(opts ...Option) *DeviceCell {
	c := &DeviceCell{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// buildEmitter creates a DirectEmitter using the cell's publisher and metrics
// provider. Falls back to metrics.NopProvider{} when no provider is injected.
// Extracted from Init to keep Init's cognitive complexity within the ≤15 limit.
//
// L4 DeviceLatent: DirectPublishFailOpen is intentional — command persistence
// succeeds independently of event publish; missed events are operational
// follow-up, not request failures. Platform L1/L2 cells use FailClosed for
// audit/compliance integrity. ref: ADR 202605101800 §D6 + KG-07 decision.
func (c *DeviceCell) buildEmitter() (*outbox.DirectEmitter, error) {
	mp := c.metricsProvider
	if mp == nil {
		mp = metrics.NopProvider{}
	}
	return outbox.NewDirectEmitter(c.publisher, outbox.DirectPublishFailOpen, mp, c.clk, "devicecell", outbox.WithLogger(c.logger))
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, lifecycle hooks).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated route-group blocks. This is a permanent convention, not a
// transitional shim — slice/handler instantiation and adapter wiring stay
// hand-written.
//
// L4 Cells do not use outboxWriter (KG-07 decision). The Cell boundary
// adapts the publisher to a direct emitter for event publishing.
//
//nolint:unparam // ctx is part of the K#04 initInternal contract; unused here, used by other cells (configcore)
func (c *DeviceCell) initInternal(ctx context.Context, reg cell.Registry) error {
	durabilityMode := reg.DurabilityMode()

	// Clock must be injected via WithClock before Init.
	clock.MustHaveClock(c.clk, "devicecell.initInternal: clock required; use WithClock(clock.Real()) in assembly")

	if err := c.initDeps(durabilityMode); err != nil {
		return err
	}
	if err := c.initSlices(durabilityMode); err != nil {
		return err
	}

	// Route groups removed: cell_gen.go owns Init and renders them.
	c.registerHealthAndLifecycle(reg)

	return nil
}

// initDeps validates and resolves publisher, emitter, and cursor codec.
func (c *DeviceCell) initDeps(durabilityMode cell.DurabilityMode) error {
	// Default to in-memory device repository if none injected.
	if c.deviceRepo == nil {
		c.deviceRepo = mem.NewDeviceRepository()
		c.logger.Info("devicecell: using in-memory device repository (demo mode)")
	}

	// Publisher is required (NIL-PUB-P1). For demo mode, the composition
	// root must wrap a publisher via outbox.WrapPublisherForCell, e.g.
	//   WithDirectPublisher(outbox.WrapPublisherForCell(&outbox.DiscardPublisher{}))
	if c.publisher == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"devicecell requires publisher; use "+
				"WithDirectPublisher(outbox.WrapPublisherForCell(&outbox.DiscardPublisher{})) "+
				"from composition root for demo mode")
	}

	// Durable mode still rejects noop publishers, but direct publish remains
	// fail-open here because this example path has no transactional outbox.
	// The request succeeds once persistence succeeds; publish misses are
	// operational follow-up, not create failure.
	if err := cell.CheckNotNoop(durabilityMode, "devicecell", c.publisher); err != nil {
		return err
	}
	builtEmitter, err := c.buildEmitter()
	if err != nil {
		return err
	}
	c.emitter = builtEmitter

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if durabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
				"devicecell durable mode requires a cursor codec; "+
					"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
					"the built-in demo key is public in the source tree")
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("devicecell: using default cursor codec (demo mode)")
	}
	return nil
}

// initSlices constructs all 4 device slices and the command sweeper.
func (c *DeviceCell) initSlices(durabilityMode cell.DurabilityMode) error {
	// device-register slice
	registerSvc := deviceregister.NewService(
		c.deviceRepo, c.logger,
		deviceregister.WithEmitter(c.emitter),
		deviceregister.WithClock(c.clk),
	)
	c.registerHandler = registercontract.NewHandler(registerSvc)
	c.AddSlice(cell.NewBaseSlice("deviceregister", "devicecell", cell.L4))

	// device-command slice: uses commandtest.InMemQueue as the command store in
	// demo/example mode. For a production deployment, inject a durable adapter
	// implementing command.Queue + command.ActiveScanner via RegisterCommandQueue.
	if c.commandQueue == nil && durabilityMode == cell.DurabilityDurable {
		return fmt.Errorf("devicecell: commandtest.InMemQueue is not suitable for durable " +
			"deployments; wire a durable command.Queue adapter instead")
	}
	if c.commandQueue == nil {
		c.commandQueue = commandtest.NewInMemQueue()
	}
	cmdQueue := c.commandQueue
	commandSvc, err := devicecommand.NewService(
		cmdQueue, c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(durabilityMode == cell.DurabilityDemo),
		devicecommand.WithClock(c.clk),
	)
	if err != nil {
		return fmt.Errorf("device-command: %w", err)
	}
	// enqueue: only admin/operator may send commands to devices.
	c.commandEnqueueHandler = enqueuecontract.NewHandler(commandSvc, auth.AnyRole(dto.RoleAdmin, dto.RoleOperator))
	// dequeue/report/ack/extend-lease: device polls/reports on its own commands
	// (subject == path {id}); admin and operator may also access for observability.
	c.commandDequeueHandler = dequeuecontract.NewHandler(commandSvc, auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator))
	c.commandReportHandler = reportcontract.NewHandler(commandSvc, auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator))
	c.commandAckHandler = ackcontract.NewHandler(commandSvc, auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator))
	c.commandExtendLeaseHandler = extendleasecontract.NewHandler(commandSvc, auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator))
	// internallist: /internal/v1/ path; Clients=["devicecell"] auto-injects RequireCallerCell via auth.Mount.
	// auth.clientsOnly:true → single-arg NewHandler; no policy needed, caller-cell allowlist is the guard.
	c.commandInternalHandler = internallistcontract.NewHandler(commandSvc)
	sweeper, err := kcommand.NewSweeper(cmdQueue, cmdQueue, c.clk,
		kcommand.WithSweeperInterval(30*time.Second),
		kcommand.WithSweeperOnError(func(err error) {
			c.logger.Error("device-command sweeper error", slog.Any("error", err))
		}))
	if err != nil {
		return fmt.Errorf("device-command sweeper: %w", err)
	}
	c.commandSweeper = commandruntime.NewSweeperLifecycle("devicecommand.sweeper", sweeper)
	c.AddSlice(cell.NewBaseSlice("devicecommand", "devicecell", cell.L4))

	// device-status slice
	statusSvc := devicestatus.NewService(c.deviceRepo, c.logger)
	// status: admin and operator may read any device's status; a device may only
	// read its own status (path {id} must match the token subject).
	c.statusHandler = statuscontract.NewHandler(statusSvc, auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator))
	c.AddSlice(cell.NewBaseSlice("devicestatus", "devicecell", cell.L0))

	// device-list slice
	listSvc, err := devicelist.NewService(c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(durabilityMode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("device-list: %w", err)
	}
	c.listHandler = listcontract.NewHandler(listSvc, auth.AnyRole("admin"))
	c.AddSlice(cell.NewBaseSlice("devicelist", "devicecell", cell.L0))
	return nil
}

// registerHealthAndLifecycle registers health probes and the sweeper lifecycle hook.
func (c *DeviceCell) registerHealthAndLifecycle(reg cell.Registry) {
	if hc, ok := c.emitter.(cell.HealthProber); ok {
		for k, v := range hc.Probes() {
			reg.Health(k, v)
		}
	}
	reg.Lifecycle(c.commandSweeper.Hook())
}
