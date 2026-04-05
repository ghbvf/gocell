// Package deviceregister implements the device-register slice: registering
// devices and publishing device.registered events.
package deviceregister

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/uid"
)

// TopicDeviceRegistered is the canonical event topic for device registration events.
const TopicDeviceRegistered = "event.device-registered.v1"

// Service handles device registration business logic.
type Service struct {
	repo      domain.DeviceRepository
	publisher outbox.Publisher
	logger    *slog.Logger
}

// NewService creates a device-register Service.
func NewService(repo domain.DeviceRepository, publisher outbox.Publisher, logger *slog.Logger) *Service {
	return &Service{
		repo:      repo,
		publisher: publisher,
		logger:    logger,
	}
}

// Register creates a new device and publishes a device.registered event.
func (s *Service) Register(ctx context.Context, name string) (*domain.Device, error) {
	if name == "" {
		return nil, errcode.New(errcode.ErrValidationFailed, "device name must not be empty")
	}

	device := &domain.Device{
		ID:       uid.NewWithPrefix("dev"),
		Name:     name,
		Status:   "online",
		LastSeen: time.Now(),
	}

	if err := s.repo.Create(ctx, device); err != nil {
		return nil, fmt.Errorf("device-register: persist: %w", err)
	}

	// L4 Cell uses publisher.Publish directly (no outboxWriter per KG-07).
	payload, err := json.Marshal(device)
	if err != nil {
		s.logger.Error("device-register: marshal event failed", slog.Any("error", err))
		return device, nil
	}

	if err := s.publisher.Publish(ctx, TopicDeviceRegistered, payload); err != nil {
		s.logger.Error("device-register: publish event failed",
			slog.String("device_id", device.ID),
			slog.Any("error", err),
		)
	} else {
		s.logger.Info("device-register: event published",
			slog.String("device_id", device.ID),
			slog.String("topic", TopicDeviceRegistered),
		)
	}

	return device, nil
}
