// Package deviceregister implements the device-register slice: registering
// devices and publishing device.registered events.
package deviceregister

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecert"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TopicDeviceRegistered is the canonical event topic for device registration events.
const TopicDeviceRegistered = "event.device-registered.v1"

// certValidity is the lifetime of a freshly-issued device certificate. A device
// registers healthy (far from expiry); the cert-renewal reconcile loop only acts
// once "now" enters certRenewalThreshold of NotAfter. A short-lived device cert
// validity (90d) with a 30d renewal threshold is a representative L4 policy.
const certValidity = 90 * 24 * time.Hour

// deviceRegisteredEvent is the event payload DTO for device registration events,
// decoupled from the domain model.
type deviceRegisteredEvent struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

func toDeviceRegisteredEvent(d *domain.Device) deviceRegisteredEvent {
	return deviceRegisteredEvent{
		ID: d.ID, Name: d.Name, Status: d.Status, LastSeen: d.LastSeen,
	}
}

// Service handles device registration business logic.
type Service struct {
	repo      domain.DeviceRepository `gocell:"required"`
	certStore *devicecert.Store       `gocell:"required"`
	emitter   outbox.CellEmitter
	logger    *slog.Logger
	clock     clock.Clock
}

// Option configures a device-register Service.
type Option func(*Service)

// WithEmitter sets the event emitter. Accepts outbox.CellEmitter (sealed
// marker); callers in _test.go may use outbox.WrapEmitterForCell(e).
func WithEmitter(e outbox.CellEmitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithCertStore sets the cell-internal certificate store seeded with an initial
// cert per registered device. Required: the cert-renewal reconcile loop scans it.
// Accumulative: a nil store leaves the previously-set value in place.
func WithCertStore(cs *devicecert.Store) Option {
	return func(s *Service) {
		if cs != nil {
			s.certStore = cs
		}
	}
}

// NewService creates a device-register Service. Returns an error if any required
// dependency is nil (repo, certStore).
func NewService(clk clock.Clock, repo domain.DeviceRepository, logger *slog.Logger, opts ...Option) (*Service, error) {
	clock.MustHaveClock(clk, "deviceregister.NewService")
	s := &Service{
		repo:    repo,
		emitter: outbox.DemoCellEmitter(),
		logger:  logger,
		clock:   clk,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Register implements registercontract.Service: decodes the generated request,
// delegates to registerInternal, and wraps the result in the generated response.
func (s *Service) Register(ctx context.Context, req *registercontract.Request) (registercontract.RegisterResponseObject, error) {
	device, err := s.registerInternal(ctx, req.Name)
	if err != nil {
		return nil, err
	}
	return registercontract.Register201JSONResponse{
		Data: &registercontract.ResponseData{
			ID:     device.ID,
			Name:   device.Name,
			Status: device.Status,
		},
	}, nil
}

// registerInternal creates a new device and publishes a device.registered event.
func (s *Service) registerInternal(ctx context.Context, name string) (*domain.Device, error) {
	if name == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "device name must not be empty")
	}

	device := &domain.Device{
		ID:       "dev" + "-" + uuid.NewString(),
		Name:     name,
		Status:   "online",
		LastSeen: s.clock.Now(),
	}

	if err := s.repo.Create(ctx, device); err != nil {
		return nil, fmt.Errorf("device-register: persist: %w", err)
	}

	// Issue the device's initial certificate (epoch 1) so the cert-renewal loop
	// has near-expiry state to observe as the cert ages toward NotAfter.
	if _, err := s.certStore.Issue(ctx, device.ID, s.clock.Now().Add(certValidity)); err != nil {
		return nil, fmt.Errorf("device-register: issue cert: %w", err)
	}

	payload, err := json.Marshal(toDeviceRegisteredEvent(device))
	if err != nil {
		s.logger.Error("device-register: marshal event failed", slog.Any("error", err))
		return device, nil
	}
	// WithOccurredAt pins the domain event time to the device registration
	// instant (device.LastSeen is set to s.clock.Now() above, == registration
	// time here), matching the todoorder example's ordercreate/orderconfirm pattern.
	// Without it occurredAt would default to the entry's seal time; both examples
	// stamp the domain time explicitly so the published wire envelope carries the
	// business event time, not the outbox INSERT time.
	entry, err := outbox.NewEntry(s.clock, ctx, TopicDeviceRegistered, payload,
		outbox.WithOccurredAt(device.LastSeen))
	if err != nil {
		s.logger.Error("device-register: build event failed", slog.Any("error", err))
		return nil, fmt.Errorf("device-register: build event: %w", err)
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		s.logger.Error(
			"device-register: publish event failed",
			slog.String("device_id", device.ID),
			slog.Any("error", err),
		)
		return nil, fmt.Errorf("device-register: emit event: %w", err)
	}

	return device, nil
}

// Ensure Service implements the generated interface at compile time.
var _ registercontract.Service = (*Service)(nil)
