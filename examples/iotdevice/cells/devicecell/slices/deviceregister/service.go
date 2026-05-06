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

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	registercontract "github.com/ghbvf/gocell/generated/contracts/http/device/register/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TopicDeviceRegistered is the canonical event topic for device registration events.
const TopicDeviceRegistered = "event.device-registered.v1"

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
	repo    domain.DeviceRepository
	emitter outbox.Emitter
	logger  *slog.Logger
	clock   clock.Clock
}

// Option configures a device-register Service.
type Option func(*Service)

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithClock sets the clock used for device timestamps. Defaults to
// clock.Real() when not provided.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		if clk != nil {
			s.clock = clk
		}
	}
}

// NewService creates a device-register Service.
func NewService(repo domain.DeviceRepository, logger *slog.Logger, opts ...Option) *Service {
	s := &Service{
		repo:    repo,
		emitter: outbox.NewNoopEmitter(),
		logger:  logger,
		clock:   clock.Real(),
	}
	for _, o := range opts {
		o(s)
	}
	return s
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

	payload, err := json.Marshal(toDeviceRegisteredEvent(device))
	if err != nil {
		s.logger.Error("device-register: marshal event failed", slog.Any("error", err))
		return device, nil
	}
	entry := outbox.Entry{
		ID:        outbox.MustNewEntryID(),
		EventType: TopicDeviceRegistered,
		Payload:   payload,
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
