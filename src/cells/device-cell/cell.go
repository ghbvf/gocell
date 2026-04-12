// Package devicecell implements the device-cell Cell for the iot-device example.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by devices on their own schedule.
package devicecell

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
	devicecommand "github.com/ghbvf/gocell/cells/device-cell/slices/device-command"
	deviceregister "github.com/ghbvf/gocell/cells/device-cell/slices/device-register"
	devicestatus "github.com/ghbvf/gocell/cells/device-cell/slices/device-status"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
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

	if c.publisher == nil {
		c.logger.Warn("device-cell: no publisher injected, events will not be published")
	}

	// device-register slice
	registerSvc := deviceregister.NewService(c.deviceRepo, c.publisher, c.logger)
	c.registerHandler = deviceregister.NewHandler(registerSvc)
	c.AddSlice(cell.NewBaseSlice("device-register", "device-cell", cell.L4))

	// Default cursor codec for pagination if not injected.
	if c.cursorCodec == nil {
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("device-cell: using default cursor codec (demo mode)")
	}

	// device-command slice
	commandSvc := devicecommand.NewService(c.commandRepo, c.deviceRepo, c.cursorCodec, c.logger)
	c.commandHandler = devicecommand.NewHandler(commandSvc)
	c.AddSlice(cell.NewBaseSlice("device-command", "device-cell", cell.L4))

	// device-status slice
	statusSvc := devicestatus.NewService(c.deviceRepo, c.logger)
	c.statusHandler = devicestatus.NewHandler(statusSvc)
	c.AddSlice(cell.NewBaseSlice("device-status", "device-cell", cell.L0))

	return nil
}

// RegisterRoutes registers HTTP routes for device-cell.
func (c *DeviceCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		v1.Route("/devices", func(devices cell.RouteMux) {
			devices.Handle("POST /", http.HandlerFunc(c.registerHandler.HandleRegister))
			devices.Handle("GET /{id}/status", http.HandlerFunc(c.statusHandler.HandleGetStatus))
			devices.Handle("POST /{id}/commands", http.HandlerFunc(c.commandHandler.HandleEnqueue))
			devices.Handle("GET /{id}/commands", http.HandlerFunc(c.commandHandler.HandleListPending))
			devices.Handle("POST /{id}/commands/{cmdId}/ack", http.HandlerFunc(c.commandHandler.HandleAck))
		})
	})
}
