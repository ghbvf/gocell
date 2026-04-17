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
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/google/uuid"
)

// pendingSort defines the default sort for pending command listings (FIFO).
var pendingSort = []query.SortColumn{
	{Name: "created_at", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// Service handles device command business logic.
type Service struct {
	cmdRepo    domain.CommandRepository
	deviceRepo domain.DeviceRepository
	codec      *query.CursorCodec
	logger     *slog.Logger
	runMode    query.RunMode
}

// NewService creates a device-command Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination (list pending commands) cannot be served
// without a cursor codec. Passing nil is a caller programming error;
// NewService returns errcode.ErrCellMissingCodec so the cell Init() can
// propagate a structured error instead of a runtime panic.
func NewService(cmdRepo domain.CommandRepository, deviceRepo domain.DeviceRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.ErrCellMissingCodec,
			"device-command: cursor codec is required")
	}
	return &Service{
		cmdRepo:    cmdRepo,
		deviceRepo: deviceRepo,
		codec:      codec,
		logger:     logger,
		runMode:    runMode,
	}, nil
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
		ID:        "cmd" + "-" + uuid.NewString(),
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

// ListPending returns a paginated page of pending commands for the given device.
// Sort: created_at ASC, id ASC (FIFO -- oldest pending commands first).
// This is the poll endpoint used by devices in the L4 latent model.
func (s *Service) ListPending(ctx context.Context, deviceID string, pageReq query.PageRequest) (query.PageResult[*domain.Command], error) {
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return query.PageResult[*domain.Command]{}, fmt.Errorf("device-command: lookup device: %w", err)
	}
	qctx := query.QueryContext("endpoint", "device-command", "deviceId", deviceID)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.Command]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     pendingSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.Command, error) {
			cmds, err := s.cmdRepo.ListPending(ctx, deviceID, params)
			if err != nil {
				return nil, fmt.Errorf("device-command: list pending: %w", err)
			}
			return cmds, nil
		},
		Extract: func(c *domain.Command) []any {
			return []any{c.CreatedAt.Format(time.RFC3339Nano), c.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "device-command"),
		RunMode:     s.runMode,
	})
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
