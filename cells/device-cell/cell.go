// Package devicecell implements the device-cell Cell for the iot-device example.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by devices on their own schedule.
package devicecell

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	devicecommand "github.com/ghbvf/gocell/cells/device-cell/slices/devicecommand"
	deviceregister "github.com/ghbvf/gocell/cells/device-cell/slices/deviceregister"
	devicestatus "github.com/ghbvf/gocell/cells/device-cell/slices/devicestatus"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
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

// WithCommandRepository sets the command repository.
func WithCommandRepository(r domain.CommandRepository) Option {
	return func(c *DeviceCell) { c.commandRepo = r }
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

// DeviceCell is the device-cell Cell implementation.
type DeviceCell struct {
	*cell.BaseCell
	deviceRepo  domain.DeviceRepository
	commandRepo domain.CommandRepository
	publisher   outbox.Publisher
	cursorCodec *query.CursorCodec
	logger      *slog.Logger

	registerHandler *deviceregister.Handler
	commandHandler  *devicecommand.Handler
	statusHandler   *devicestatus.Handler
}

// NewDeviceCell creates a new DeviceCell with the given options.
func NewDeviceCell(opts ...Option) *DeviceCell {
	c := &DeviceCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "device-cell",
			Type:             cell.CellTypeEdge,
			ConsistencyLevel: cell.L4,
			Owner:            cell.Owner{Team: "examples", Role: "device-owner"},
			Schema:           cell.SchemaConfig{Primary: "devices"},
			Verify:           cell.CellVerify{Smoke: []string{"device-cell/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Init sets up repositories, slice services, and handlers.
// L4 Cells do not use outboxWriter (KG-07 decision). The publisher is used
// directly for event publishing.
func (c *DeviceCell) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Default to in-memory repositories if none injected.
	if c.deviceRepo == nil {
		c.deviceRepo = mem.NewDeviceRepository()
		c.logger.Info("device-cell: using in-memory device repository (demo mode)")
	}
	if c.commandRepo == nil {
		c.commandRepo = mem.NewCommandRepository()
		c.logger.Info("device-cell: using in-memory command repository (demo mode)")
	}

	// Publisher is required (NIL-PUB-P1). Use &DiscardPublisher{} for demo mode.
	if c.publisher == nil {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"device-cell requires publisher; use WithPublisher(&outbox.DiscardPublisher{}) for demo mode")
	}

	// Durable mode: reject noop publisher (#27c-2).
	if err := cell.CheckNotNoop(deps.DurabilityMode, "device-cell", c.publisher); err != nil {
		return err
	}

	// device-register slice
	registerSvc := deviceregister.NewService(c.deviceRepo, c.publisher, c.logger)
	c.registerHandler = deviceregister.NewHandler(registerSvc)
	c.AddSlice(cell.NewBaseSlice("deviceregister", "device-cell", cell.L4))

	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if c.cursorCodec == nil {
		if deps.DurabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.ErrCellMissingCodec,
				"device-cell durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
		}
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("device-cell: using default cursor codec (demo mode)")
	}

	// device-command slice
	commandSvc, err := devicecommand.NewService(c.commandRepo, c.deviceRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("device-command: %w", err)
	}
	c.commandHandler = devicecommand.NewHandler(commandSvc)
	c.AddSlice(cell.NewBaseSlice("devicecommand", "device-cell", cell.L4))

	// device-status slice
	statusSvc := devicestatus.NewService(c.deviceRepo, c.logger)
	c.statusHandler = devicestatus.NewHandler(statusSvc)
	c.AddSlice(cell.NewBaseSlice("devicestatus", "device-cell", cell.L0))

	return nil
}

// RegisterRoutes registers HTTP routes for device-cell.
func (c *DeviceCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		v1.Route("/devices", func(devices cell.RouteMux) {
			// Device self-registration is a public endpoint: devices bootstrap
			// without a user JWT; the caller identifies itself in the request body.
			auth.Declare(devices, auth.RouteDecl{
				Method:  "POST",
				Path:    "/",
				Handler: http.HandlerFunc(c.registerHandler.HandleRegister),
				Public:  true,
			})
			// Device status is queried by authenticated operators/devices.
			auth.Declare(devices, auth.RouteDecl{
				Method:  "GET",
				Path:    "/{id}/status",
				Handler: http.HandlerFunc(c.statusHandler.HandleGetStatus),
				Policy:  auth.Authenticated(),
			})
			// device-command routes: no route-level policy. Pre-F3 device-cell
			// had no policy wrapping; restoring Policy:nil matches that state.
			// When a deployment wants authz, wire WithAuthDiscovery() and add a
			// Policy or rely on AuthMiddleware's baseline JWT check.
			// Hardening device-cell authz is out of scope for the F3 migration.
			auth.Declare(devices, auth.RouteDecl{
				Method:  "POST",
				Path:    "/{id}/commands",
				Handler: http.HandlerFunc(c.commandHandler.HandleEnqueue),
			})
			auth.Declare(devices, auth.RouteDecl{
				Method:  "GET",
				Path:    "/{id}/commands",
				Handler: http.HandlerFunc(c.commandHandler.HandleListPending),
			})
			auth.Declare(devices, auth.RouteDecl{
				Method:  "POST",
				Path:    "/{id}/commands/{cmdId}/ack",
				Handler: http.HandlerFunc(c.commandHandler.HandleAck),
			})
		})
	})
}
