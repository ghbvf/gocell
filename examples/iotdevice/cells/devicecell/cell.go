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
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcommand "github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
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

// Compile-time interface check.
var _ cell.Cell = (*DeviceCell)(nil)

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

// WithPublisher sets the outbox Publisher for event publishing.
func WithPublisher(p outbox.Publisher) Option {
	return func(c *DeviceCell) { c.publisher = p }
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
type DeviceCell struct {
	*cell.BaseCell
	deviceRepo      domain.DeviceRepository
	publisher       outbox.Publisher
	emitter         outbox.Emitter // set during Init; retained for Probes
	cursorCodec     *query.CursorCodec
	logger          *slog.Logger
	metricsProvider metrics.Provider
	commandQueue    commandQueueStore
	commandSweeper  *commandruntime.SweeperLifecycle
	clk             clock.Clock // injected from reg.Config during Init

	registerHandler *deviceregister.Handler
	commandHandler  *devicecommand.Handler
	statusHandler   *devicestatus.Handler
	listHandler     *devicelist.Handler
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
		BaseCell: cell.NewBaseCell(&metadata.CellMeta{
			ID:               "devicecell",
			Type:             "edge",
			ConsistencyLevel: "L4",
			DurabilityMode:   "durable",
			Owner:            metadata.OwnerMeta{Team: "examples", Role: "device-owner"},
			Schema:           metadata.SchemaMeta{Primary: "devices"},
			Verify:           metadata.CellVerifyMeta{Smoke: []string{"devicecell/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// buildEmitter creates a DirectEmitter using the cell's publisher and metrics
// provider. Falls back to metrics.NopProvider{} when no provider is injected.
// Extracted from Init to keep Init's cognitive complexity within the ≤15 limit.
func (c *DeviceCell) buildEmitter() (*outbox.DirectEmitter, error) {
	mp := c.metricsProvider
	if mp == nil {
		mp = metrics.NopProvider{}
	}
	return outbox.NewDirectEmitter(c.publisher, outbox.DirectPublishFailOpen, mp, c.clk, "devicecell", outbox.WithLogger(c.logger))
}

// Init sets up repositories, slice services, handlers, and registers routes,
// lifecycle hooks, and health probes into reg.
// L4 Cells do not use outboxWriter (KG-07 decision). The Cell boundary
// adapts the publisher to a direct emitter for event publishing.
func (c *DeviceCell) Init(ctx context.Context, reg cell.Registry) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}

	durabilityMode := reg.DurabilityMode()

	// Clock must be injected via WithClock before Init.
	clock.MustHaveClock(c.clk, "devicecell.Init: clock required; use WithClock(clock.Real()) in assembly")

	if err := c.initDeps(durabilityMode); err != nil {
		return err
	}
	if err := c.initSlices(durabilityMode); err != nil {
		return err
	}

	c.registerRouteGroups(reg)
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

	// Publisher is required (NIL-PUB-P1). Use &DiscardPublisher{} for demo mode.
	if c.publisher == nil {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"devicecell requires publisher; use WithPublisher(&outbox.DiscardPublisher{}) for demo mode")
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
			return errcode.New(errcode.ErrCellMissingCodec,
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
	registerSvc := deviceregister.NewService(c.deviceRepo, c.logger,
		deviceregister.WithEmitter(c.emitter),
		deviceregister.WithClock(c.clk),
	)
	c.registerHandler = deviceregister.NewHandler(registerSvc)
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
	commandSvc, err := devicecommand.NewService(cmdQueue, c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(durabilityMode == cell.DurabilityDemo),
		devicecommand.WithClock(c.clk),
	)
	if err != nil {
		return fmt.Errorf("device-command: %w", err)
	}
	c.commandHandler = devicecommand.NewHandler(commandSvc)
	c.commandSweeper = commandruntime.NewSweeperLifecycle("devicecommand.sweeper", &kcommand.Sweeper{
		Scanner:  cmdQueue,
		Queue:    cmdQueue,
		Filter:   kcommand.ScanFilter{},
		Interval: 30 * time.Second,
		OnError: func(err error) {
			c.logger.Error("device-command sweeper error", slog.Any("error", err))
		},
	})
	c.AddSlice(cell.NewBaseSlice("devicecommand", "devicecell", cell.L4))

	// device-status slice
	statusSvc := devicestatus.NewService(c.deviceRepo, c.logger)
	c.statusHandler = devicestatus.NewHandler(statusSvc)
	c.AddSlice(cell.NewBaseSlice("devicestatus", "devicecell", cell.L0))

	// device-list slice
	listSvc, err := devicelist.NewService(c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(durabilityMode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("device-list: %w", err)
	}
	c.listHandler = devicelist.NewHandler(listSvc)
	c.AddSlice(cell.NewBaseSlice("devicelist", "devicecell", cell.L0))
	return nil
}

// registerRouteGroups registers primary and internal HTTP route groups.
func (c *DeviceCell) registerRouteGroups(reg cell.Registry) {
	reg.RouteGroup(cell.RouteGroup{
		Listener: cell.PrimaryListener,
		// Empty prefix: contract specs already carry absolute /api/v1/...
		// paths, so we mount routes directly on the root mux without an
		// outer Route("/api/v1") wrapper that would double-prefix.
		Prefix: "",
		Register: func(mux cell.RouteMux) error {
			var firstErr error
			captureErr := func(err error) {
				if err != nil && firstErr == nil {
					firstErr = err
				}
			}
			mux.Route("/api/v1/devices", func(devices cell.RouteMux) {
				captureErr(c.registerHandler.RegisterRoutes(devices))
				captureErr(c.listHandler.RegisterRoutes(devices))
				captureErr(c.statusHandler.RegisterRoutes(devices))
				// device-command public routes (enqueue, dequeue, report, ack,
				// extend-lease) live under /api/v1/devices/{id}/commands.
				captureErr(c.commandHandler.RegisterRoutes(devices))
			})
			return firstErr
		},
	})
	reg.RouteGroup(cell.RouteGroup{
		Listener: cell.InternalListener,
		Prefix:   "",
		Register: func(mux cell.RouteMux) error {
			return c.commandHandler.RegisterInternalRoutes(mux)
		},
	})
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
