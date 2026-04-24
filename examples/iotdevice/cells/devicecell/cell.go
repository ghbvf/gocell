// Package devicecell implements the devicecell Cell for the iotdevice example.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by devices on their own schedule.
package devicecell

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	devicecommand "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicecommand"
	devicelist "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicelist"
	deviceregister "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/deviceregister"
	devicestatus "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/slices/devicestatus"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time interface checks.
var (
	_ cell.Cell          = (*DeviceCell)(nil)
	_ cell.HTTPRegistrar = (*DeviceCell)(nil)
)

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

// DeviceCell is the devicecell Cell implementation.
type DeviceCell struct {
	*cell.BaseCell
	deviceRepo  domain.DeviceRepository
	publisher   outbox.Publisher
	cursorCodec *query.CursorCodec
	logger      *slog.Logger

	registerHandler *deviceregister.Handler
	commandHandler  *devicecommand.Handler
	statusHandler   *devicestatus.Handler
	listHandler     *devicelist.Handler
}

// NewDeviceCell creates a new DeviceCell with the given options.
func NewDeviceCell(opts ...Option) *DeviceCell {
	c := &DeviceCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "devicecell",
			Type:             cell.CellTypeEdge,
			ConsistencyLevel: cell.L4,
			Owner:            cell.Owner{Team: "examples", Role: "device-owner"},
			Schema:           cell.SchemaConfig{Primary: "devices"},
			Verify:           cell.CellVerify{Smoke: []string{"devicecell/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Init sets up repositories, slice services, and handlers.
// L4 Cells do not use outboxWriter (KG-07 decision). The Cell boundary
// adapts the publisher to a direct emitter for event publishing.
func (c *DeviceCell) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

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
	if err := cell.CheckNotNoop(deps.DurabilityMode, "devicecell", c.publisher); err != nil {
		return err
	}
	emitter, err := outbox.NewDirectEmitter(c.publisher, outbox.DirectPublishFailOpen, c.logger)
	if err != nil {
		return err
	}

	// device-register slice
	registerSvc := deviceregister.NewService(c.deviceRepo, c.logger, deviceregister.WithEmitter(emitter))
	c.registerHandler = deviceregister.NewHandler(registerSvc)
	c.AddSlice(cell.NewBaseSlice("deviceregister", "devicecell", cell.L4))

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if deps.DurabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.ErrCellMissingCodec,
				"devicecell durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("devicecell: using default cursor codec (demo mode)")
	}

	// device-command slice: uses commandtest.InMemQueue as the command store in
	// demo/example mode. commandtest is importable from production code (regular
	// .go file, not _test.go). For a production deployment, replace with an
	// adapter implementing command.Queue + command.Reader + command.StateAdvancer.
	//
	// TODO(PR-A12-SWEEPER-WIRE): wire command.Sweeper once InMemQueue supports
	// multi-device scan (PendingAll). Tracked in backlog PR-A12-SWEEPER-WIRE.
	if deps.DurabilityMode == cell.DurabilityDurable {
		return fmt.Errorf("devicecell: commandtest.InMemQueue is not suitable for durable deployments; wire a durable command.Queue adapter instead")
	}
	cmdQueue := commandtest.NewInMemQueue()
	commandSvc, err := devicecommand.NewService(cmdQueue, c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("device-command: %w", err)
	}
	c.commandHandler = devicecommand.NewHandler(commandSvc)
	c.AddSlice(cell.NewBaseSlice("devicecommand", "devicecell", cell.L4))

	// device-status slice
	statusSvc := devicestatus.NewService(c.deviceRepo, c.logger)
	c.statusHandler = devicestatus.NewHandler(statusSvc)
	c.AddSlice(cell.NewBaseSlice("devicestatus", "devicecell", cell.L0))

	// device-list slice
	listSvc, err := devicelist.NewService(c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("device-list: %w", err)
	}
	c.listHandler = devicelist.NewHandler(listSvc)
	c.AddSlice(cell.NewBaseSlice("devicelist", "devicecell", cell.L0))

	return nil
}

// Contract spec literals for devicecell. Examples are not backed by a
// contracts/**/contract.yaml (FMT-18 exempts examples/**), but the Mount
// registration still enforces Method/Path shape at startup.
var (
	specDeviceRegister = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.register.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/",
	}
	specDeviceList = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/devices/",
	}
	specDeviceStatus = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.status.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/devices/{id}/status",
	}
	specDeviceCommandEnqueue = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.commands.enqueue.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands",
	}
	specDeviceCommandList = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.commands.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/devices/{id}/commands",
	}
	specDeviceCommandAck = wrapper.ContractSpec{
		ID: "http.iotdevice.devices.commands.ack.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/devices/{id}/commands/{cmdId}/ack",
	}
)

// RegisterRoutes registers HTTP routes for devicecell.
func (c *DeviceCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		v1.Route("/devices", func(devices cell.RouteMux) {
			// Device self-registration is a public endpoint: devices bootstrap
			// without a user JWT; the caller identifies itself in the request body.
			auth.Mount(devices, auth.Route{
				Contract: specDeviceRegister,
				Handler:  http.HandlerFunc(c.registerHandler.HandleRegister),
				Public:   true,
			})
			// Device list: paginated listing at /api/v1/devices/.
			auth.Mount(devices, auth.Route{
				Contract: specDeviceList,
				Handler:  http.HandlerFunc(c.listHandler.HandleList),
				Policy:   auth.AnyRole("admin"),
			})
			// Device status is queried by authenticated operators/devices.
			auth.Mount(devices, auth.Route{
				Contract: specDeviceStatus,
				Handler:  http.HandlerFunc(c.statusHandler.HandleGetStatus),
				Policy:   auth.Authenticated(),
			})
			// device-command routes: no route-level policy (pre-F3 devicecell
			// had no policy wrapping). Deployments wanting authz wire
			// WithAuthDiscovery and add a Policy or rely on AuthMiddleware's
			// baseline JWT check.
			auth.Mount(devices, auth.Route{
				Contract: specDeviceCommandEnqueue,
				Handler:  http.HandlerFunc(c.commandHandler.HandleEnqueue),
			})
			auth.Mount(devices, auth.Route{
				Contract: specDeviceCommandList,
				Handler:  http.HandlerFunc(c.commandHandler.HandleListPending),
			})
			auth.Mount(devices, auth.Route{
				Contract: specDeviceCommandAck,
				Handler:  http.HandlerFunc(c.commandHandler.HandleAck),
			})
		})
	})
}
