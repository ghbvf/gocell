// Package devicestatus implements the device-status slice: querying device status.
package devicestatus

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
)

// Service handles device status query business logic.
type Service struct {
	repo   domain.DeviceRepository
	logger *slog.Logger
}

// NewService creates a device-status Service.
func NewService(repo domain.DeviceRepository, logger *slog.Logger) *Service {
	return &Service{
		repo:   repo,
		logger: logger,
	}
}

// GetStatus returns the current status of a device.
func (s *Service) GetStatus(ctx context.Context, id string) (*domain.Device, error) {
	return s.repo.GetByID(ctx, id)
}
