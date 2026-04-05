// Package devicecommand implements the device-command slice: enqueuing
// commands for devices, polling pending commands, and acknowledging execution.
// This is an application-level L4 command primitive. Framework-level
// kernel/command first-class support is planned for v1.1.
package devicecommand

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/uid"
)

// Service handles device command business logic.
type Service struct {
	cmdRepo    domain.CommandRepository
	deviceRepo domain.DeviceRepository
	logger     *slog.Logger
}

// NewService creates a device-command Service.
func NewService(cmdRepo domain.CommandRepository, deviceRepo domain.DeviceRepository, logger *slog.Logger) *Service {
	return &Service{
		cmdRepo:    cmdRepo,
		deviceRepo: deviceRepo,
		logger:     logger,
	}
}

// Enqueue creates a new pending command for the given device.
func (s *Service) Enqueue(ctx context.Context, deviceID, payload string) (*domain.Command, error) {
	// Verify device exists.
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return nil, fmt.Errorf("device-command: lookup device: %w", err)
	}

	if payload == "" {
		return nil, errcode.New(errcode.ErrValidationFailed, "command payload must not be empty")
	}

	cmd := &domain.Command{
		ID:        uid.NewWithPrefix("cmd"),
		DeviceID:  deviceID,
		Payload:   payload,
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	if err := s.cmdRepo.Create(ctx, cmd); err != nil {
		return nil, fmt.Errorf("device-command: persist: %w", err)
	}

	s.logger.Info("device-command: command enqueued",
		slog.String("command_id", cmd.ID),
		slog.String("device_id", deviceID),
	)
	return cmd, nil
}

// ListPending returns all pending commands for the given device.
// This is the poll endpoint used by devices in the L4 latent model.
func (s *Service) ListPending(ctx context.Context, deviceID string) ([]*domain.Command, error) {
	// Verify device exists.
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return nil, fmt.Errorf("device-command: lookup device: %w", err)
	}

	cmds, err := s.cmdRepo.ListPending(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("device-command: list pending: %w", err)
	}
	return cmds, nil
}

// Ack acknowledges that a device has executed a command.
func (s *Service) Ack(ctx context.Context, deviceID, cmdID string) error {
	if err := s.cmdRepo.Ack(ctx, deviceID, cmdID); err != nil {
		return fmt.Errorf("device-command: ack: %w", err)
	}

	s.logger.Info("device-command: command acknowledged",
		slog.String("command_id", cmdID),
		slog.String("device_id", deviceID),
	)
	return nil
}
