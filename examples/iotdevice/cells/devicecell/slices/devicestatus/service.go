// Package devicestatus implements the device-status slice: querying device status.
package devicestatus

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	statuscontract "github.com/ghbvf/gocell/generated/contracts/http/device/status/v1"
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

// Status implements statuscontract.Service: retrieves a device by ID and wraps
// the result in the generated response type.
func (s *Service) Status(ctx context.Context, req *statuscontract.Request) (*statuscontract.Response, error) {
	device, err := s.GetStatus(ctx, req.ID)
	if err != nil {
		return nil, err
	}
	return &statuscontract.Response{
		Data: &statuscontract.ResponseData{
			ID:       device.ID,
			Name:     device.Name,
			Status:   device.Status,
			LastSeen: device.LastSeen.Format(time.RFC3339),
		},
	}, nil
}

// GetStatus returns the current status of a device.
func (s *Service) GetStatus(ctx context.Context, id string) (*domain.Device, error) {
	return s.repo.GetByID(ctx, id)
}

// Ensure Service implements the generated interface at compile time.
var _ statuscontract.Service = (*Service)(nil)
