// Package devicecommand implements the device-command slice: enqueuing
// commands for devices, polling pending commands, and acknowledging execution.
// This slice uses kernel/command primitives (L4 DeviceLatent model).
package devicecommand

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// pendingSort defines the default sort for pending command listings (FIFO).
var pendingSort = []query.SortColumn{
	{Name: "created_at", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// commandQueueReader combines the Queue facade with the GetCommand lookup
// needed for ownership checks and idempotent Ack. commandtest.InMemQueue
// satisfies this interface; a postgres adapter would implement it too.
type commandQueueReader interface {
	command.Queue
	GetCommand(ctx context.Context, id string) (*command.Entry, error)
}

// commandQueueAdvancer extends commandQueueReader with StateAdvancer for
// the Pending→Sent→Delivered chaining required by the HTTP-poll Ack flow.
type commandQueueAdvancer interface {
	commandQueueReader
	command.StateAdvancer
}

// Service handles device command business logic.
//
// NewService accepts any commandQueueAdvancer; in demo/example mode this is
// commandtest.InMemQueue. A production postgres adapter would provide the
// same combined interface.
type Service struct {
	queue      commandQueueAdvancer
	deviceRepo domain.DeviceRepository
	codec      *query.CursorCodec
	logger     *slog.Logger
	runMode    query.RunMode

	// authz is the optional T3 DEVICE-ENQUEUE-RBAC hook. Nil means no authz
	// check (demo mode). Deployments that need role-based control set this via
	// WithAuthz option or direct assignment.
	authz command.AuthzFunc
}

// NewService creates a device-command Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination (list pending commands) cannot be served
// without a cursor codec. Passing nil is a caller programming error;
// NewService returns errcode.ErrCellMissingCodec so the cell Init() can
// propagate a structured error instead of a runtime panic.
func NewService(q commandQueueAdvancer, deviceRepo domain.DeviceRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.ErrCellMissingCodec,
			"device-command: cursor codec is required")
	}
	return &Service{
		queue:      q,
		deviceRepo: deviceRepo,
		codec:      codec,
		logger:     logger,
		runMode:    runMode,
	}, nil
}

// generateID generates a random hex command ID.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("device-command: generate ID: %w", err)
	}
	return "cmd-" + hex.EncodeToString(b), nil
}

// Enqueue creates a new pending command for the given device.
//
// commandType defaults to "default" when empty — callers that don't specify
// a type (e.g. early demo scripts) get a sensible fallback without error.
// T3 DEVICE-ENQUEUE-RBAC: s.authz is called when non-nil to enforce RBAC.
// Authz is checked before device lookup to prevent timing-based information
// leakage (403 must precede 404 so callers cannot probe device existence).
// L4 consistency: Enqueue is a Pending write (no outbox required at this stage).
func (s *Service) Enqueue(ctx context.Context, deviceID, commandType, payload string) (command.Entry, error) {
	// Authz check before any data access — prevents 404 vs 403 timing probing.
	if s.authz != nil {
		if err := s.authz(ctx); err != nil {
			return command.Entry{}, errcode.Wrap(errcode.ErrAuthForbidden,
				"device-command: enqueue authorization failed", err)
		}
	}

	// Verify device exists.
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return command.Entry{}, fmt.Errorf("device-command: lookup device: %w", err)
	}

	if payload == "" {
		return command.Entry{}, errcode.New(errcode.ErrValidationFailed, "command payload must not be empty")
	}

	// Default commandType to "default" for backward-compat demo callers.
	if commandType == "" {
		commandType = "default"
	}

	id, err := generateID()
	if err != nil {
		return command.Entry{}, err
	}

	entry := command.NewEntry(id, deviceID, commandType, []byte(payload), command.Timeouts{}, time.Now())

	if err := s.queue.Enqueue(ctx, entry, command.EnqueueOptions{Authz: s.authz}); err != nil {
		return command.Entry{}, fmt.Errorf("device-command: enqueue: %w", err)
	}

	s.logger.Info("device-command: command enqueued",
		slog.String("command_id", entry.ID),
		slog.String("device_id", deviceID),
		slog.String("command_type", commandType),
	)
	return entry, nil
}

// ListPending returns a paginated page of pending commands for the given device.
// Sort: created_at ASC, id ASC (FIFO -- oldest non-terminal commands first).
// This is the poll endpoint used by devices in the L4 latent model.
func (s *Service) ListPending(ctx context.Context, deviceID string, pageReq query.PageParams) (query.PageResult[command.Entry], error) {
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return query.PageResult[command.Entry]{}, fmt.Errorf("device-command: lookup device: %w", err)
	}
	qctx := query.QueryContext("endpoint", "device-command", "deviceId", deviceID)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[command.Entry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       pendingSort,
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]command.Entry, error) {
			// Load all non-terminal entries for this device, then apply in-memory
			// cursor pagination. Queue.ListPending sorts by CreatedAt ASC.
			// For large-scale backends, a native SQL cursor is preferred.
			all, err := s.queue.ListPending(ctx, deviceID, 0) // 0 = no limit
			if err != nil {
				return nil, fmt.Errorf("device-command: list pending: %w", err)
			}
			// Sort and apply cursor filter so ExecutePagedQuery receives the
			// correctly-windowed slice (including the N+1 for hasMore detection).
			query.Sort(all, params.Sort, entryFieldCompare)
			return query.ApplyCursor(all, params, entryFieldValue)
		},
		Extract: func(e command.Entry) []any {
			return []any{e.CreatedAt.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "device-command"),
		RunMode:     s.runMode,
	})
}

// entryFieldCompare compares a single named field of two command.Entry values.
// Supports the same fields used in pendingSort (created_at, id).
func entryFieldCompare(a, b command.Entry, field string) int {
	switch field {
	case "created_at":
		return a.CreatedAt.Compare(b.CreatedAt)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	case "device_id":
		return cmp.Compare(a.DeviceID, b.DeviceID)
	default:
		return 0
	}
}

// entryFieldValue extracts a cursor-comparable value from a command.Entry.
func entryFieldValue(e command.Entry, field string) any {
	switch field {
	case "created_at":
		return e.CreatedAt
	case "id":
		return e.ID
	case "device_id":
		return e.DeviceID
	default:
		return ""
	}
}

// Ack acknowledges that a device has executed a command.
//
// The HTTP-poll flow Acks directly from Pending status. The kernel Queue
// requires Delivered state before AckSuccess, so this method chains:
//
//	Pending → Sent → Delivered  (via StateAdvancer)
//	Delivered → Succeeded       (via Queue.Ack)
//
// Already-terminal commands are a no-op (idempotent).
// L4 consistency: Ack advances state; no outbox publish at this layer.
//
// NOTE: non-atomic. The chained Pending→Sent→Delivered→Succeeded advance in
// this method is composed of three independent StateAdvancer calls; between
// steps another goroutine (e.g., Sweeper) could observe an intermediate state.
// This is acceptable for demo/in-memory mode where InMemQueue's mutex makes
// conflicts impossible. Production command adapters (e.g., postgres) SHOULD
// implement this as a single transaction — see backlog PR-A12-ACK-ATOMIC.
func (s *Service) Ack(ctx context.Context, deviceID, cmdID string) error {
	now := time.Now()

	e, err := s.queue.GetCommand(ctx, cmdID)
	if err != nil {
		return fmt.Errorf("device-command: get command: %w", err)
	}
	if e == nil {
		return errcode.New(errcode.ErrCommandNotFound,
			fmt.Sprintf("device-command: command %q not found", cmdID))
	}

	// Ownership check.
	if e.DeviceID != deviceID {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("device-command: command %q does not belong to device %q", cmdID, deviceID))
	}

	// Idempotent: already terminal → no-op.
	if e.Status.IsTerminal() {
		return nil
	}

	// Chain Pending→Sent→Delivered so Queue.Ack can finalize to Succeeded.
	// StatusDelivered is the valid state for Queue.Ack(AckSuccess).
	switch e.Status {
	case command.StatusPending:
		if err := s.queue.AdvanceStatus(ctx, cmdID, command.StatusPending, command.StatusSent, now); err != nil {
			return fmt.Errorf("device-command: ack advance pending→sent: %w", err)
		}
		if err := s.queue.AdvanceStatus(ctx, cmdID, command.StatusSent, command.StatusDelivered, now); err != nil {
			return fmt.Errorf("device-command: ack advance sent→delivered: %w", err)
		}
	case command.StatusSent:
		if err := s.queue.AdvanceStatus(ctx, cmdID, command.StatusSent, command.StatusDelivered, now); err != nil {
			return fmt.Errorf("device-command: ack advance sent→delivered: %w", err)
		}
	}

	if err := s.queue.Ack(ctx, cmdID, command.AckSuccess, now); err != nil {
		return fmt.Errorf("device-command: ack: %w", err)
	}

	s.logger.Info("device-command: command acknowledged",
		slog.String("command_id", cmdID),
		slog.String("device_id", deviceID),
	)
	return nil
}
