// Package devicecmd holds the shared device-command domain logic (enqueue /
// dequeue / report / ack / extend-lease / list) consumed by two sibling slices
// that sit on different HTTP trust boundaries: the public-facing `devicecommand`
// slice (commands under /api/v1, device/operator-gated) and the internal
// control-plane `devicecommandinternal` slice (list under /internal/v1,
// caller-cell gated). Slices may not import each other, so the domain logic
// lives here and each slice creates its own Service instance with a distinct
// sliceName label. Each slice owns its own Adapter(s) that bridge the generated
// contract interface to the domain methods here.
//
// The split is enforced by governance rule SLICE-HTTP-VISIBILITY-SEGREGATION-01
// (FMT-33): a single slice must not serve both public and internal HTTP
// contracts.
package devicecmd

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// pendingSort defines the default sort for command listings (FIFO).
var pendingSort = []query.SortColumn{
	{Name: "created_at", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// MaxLeaseExtension caps one public lease extension request. Devices can renew
// repeatedly while still making abuse and accidental long leases bounded.
const MaxLeaseExtension = time.Hour

// errLookupDeviceFmt wraps a device-lookup failure; shared by Enqueue,
// Dequeue, and ScanActive (CLAUDE.md: 同义字符串 ≥ 3 次抽常量).
const errLookupDeviceFmt = "device-command: lookup device: %w"

// commandQueueStore combines the Queue facade with the ActiveScanner lookup
// needed for ownership checks, sweeper scans, and internal ops views.
// commandtest.InMemQueue satisfies this interface; a postgres adapter would
// implement it too.
type commandQueueStore interface {
	command.Queue
	command.ActiveScanner
}

// Service handles device command business logic.
//
// NewService accepts any commandQueueStore; in demo/example mode this is
// commandtest.InMemQueue. A production postgres adapter would provide the same
// combined interface.
//
// Service deliberately does NOT implement any generated contract Service
// interface. Each slice (devicecommand / devicecommandinternal) owns its own
// Adapter type that bridges the generated interface to these domain methods,
// enforcing type-level trust-boundary segregation. See
// HTTP-CONTRACT-VISIBILITY-TYPE-SEGREGATION-01 archtest.
type Service struct {
	queue      commandQueueStore
	deviceRepo domain.DeviceRepository
	codec      *query.CursorCodec
	logger     *slog.Logger
	runMode    query.RunMode
	clock      clock.Clock
	sliceName  string

	// authz is the optional T3 DEVICE-ENQUEUE-RBAC hook. Nil means no authz
	// check (demo mode). Deployments that need role-based control set this via
	// WithAuthz option or direct assignment.
	authz command.AuthzFunc
}

// Option configures a device-command Service.
type Option func(*Service)

// WithClock sets the clock used for command timestamps. Defaults to
// clock.Real() when not provided.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		if clk != nil {
			s.clock = clk
		}
	}
}

// NewService creates a device-command Service. sliceName identifies the owning
// slice in observability labels (e.g. "devicecommand" or "devicecommandinternal");
// each slice must create its own Service instance so that cursor-error logs and
// query-context labels can be attributed to the correct slice.
//
// runMode controls cursor fail-open vs fail-closed semantics; pass
// query.RunModeProd unless the assembly declares DurabilityDemo.
//
// codec must be non-nil — internal ops pagination cannot be served without a
// cursor codec. Passing nil is a caller programming error;
// NewService returns errcode.ErrCellMissingCodec so the cell Init() can
// propagate a structured error instead of a runtime panic.
func NewService(
	q commandQueueStore, deviceRepo domain.DeviceRepository,
	codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode,
	opts ...Option,
) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
			"device-command: cursor codec is required")
	}
	s := &Service{
		queue:      q,
		deviceRepo: deviceRepo,
		codec:      codec,
		logger:     logger,
		runMode:    runMode,
		clock:      clock.Real(),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// WithSliceName sets the sliceName used for observability labels (query context
// and cursor-error log tags). Each slice must pass its own slice ID so that
// logs and metrics can be attributed to the correct trust boundary.
func WithSliceName(name string) Option {
	return func(s *Service) {
		if name != "" {
			s.sliceName = name
		}
	}
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
			return command.Entry{}, errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
				"device-command: enqueue authorization failed", err)
		}
	}

	// Verify device exists.
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return command.Entry{}, fmt.Errorf(errLookupDeviceFmt, err)
	}

	if payload == "" {
		return command.Entry{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "command payload must not be empty")
	}

	// Default commandType to "default" for backward-compat demo callers.
	if commandType == "" {
		commandType = "default"
	}

	id, err := generateID()
	if err != nil {
		return command.Entry{}, err
	}

	entry := command.NewEntry(id, deviceID, commandType, []byte(payload), command.Timeouts{}, s.clock.Now())

	if err := s.queue.Enqueue(ctx, entry, command.EnqueueOptions{Authz: s.authz}); err != nil {
		return command.Entry{}, fmt.Errorf("device-command: enqueue: %w", err)
	}

	s.logger.Info(
		"device-command: command enqueued",
		slog.String("command_id", entry.ID),
		slog.String("device_id", deviceID),
		slog.String("command_type", commandType),
	)
	return entry, nil
}

// Dequeue claims pending commands for the given device and advances them to Sent.
// This is the poll endpoint used by devices in the L4 latent model.
func (s *Service) Dequeue(ctx context.Context, deviceID string, limit int, lease time.Duration) ([]command.Entry, error) {
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return nil, fmt.Errorf(errLookupDeviceFmt, err)
	}
	if limit <= 0 {
		limit = query.DefaultPageSize
	}
	if lease <= 0 {
		lease = command.DefaultLeaseDuration
	}
	entries, err := s.queue.Dequeue(ctx, deviceID, limit, lease)
	if err != nil {
		return nil, fmt.Errorf("device-command: dequeue: %w", err)
	}
	s.logger.Info(
		"device-command: commands dequeued",
		slog.String("device_id", deviceID),
		slog.Int("count", len(entries)),
	)
	return entries, nil
}

// Report records that the device has received the command and started work.
func (s *Service) Report(ctx context.Context, deviceID, cmdID string) error {
	now := s.clock.Now()
	if err := s.getOwnedCommand(ctx, deviceID, cmdID); err != nil {
		return err
	}
	if err := s.queue.Report(ctx, cmdID, now); err != nil {
		return fmt.Errorf("device-command: report: %w", err)
	}
	s.logger.Info(
		"device-command: command reported delivered",
		slog.String("command_id", cmdID),
		slog.String("device_id", deviceID),
	)
	return nil
}

// Ack finalizes a command with the supplied terminal reason. Ack is a single
// Queue transition; it does not synthesize Sent/Delivered timestamps.
func (s *Service) Ack(ctx context.Context, deviceID, cmdID string, reason command.AckReason) error {
	if !reason.Valid() {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "device-command: invalid ack reason")
	}
	now := s.clock.Now()
	if err := s.getOwnedCommand(ctx, deviceID, cmdID); err != nil {
		return err
	}

	if err := s.queue.Ack(ctx, cmdID, reason, now); err != nil {
		return fmt.Errorf("device-command: ack: %w", err)
	}

	s.logger.Info(
		"device-command: command acknowledged",
		slog.String("command_id", cmdID),
		slog.String("device_id", deviceID),
		slog.String("reason", reason.String()),
	)
	return nil
}

// ExtendLease extends an existing command lease for a device that is still
// processing a command.
func (s *Service) ExtendLease(ctx context.Context, deviceID, cmdID string, extension time.Duration) error {
	if extension <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "device-command: extension must be positive")
	}
	if extension > MaxLeaseExtension {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "device-command: extension exceeds maximum")
	}
	if err := s.getOwnedCommand(ctx, deviceID, cmdID); err != nil {
		return err
	}
	if err := s.queue.ExtendLease(ctx, cmdID, extension, s.clock.Now()); err != nil {
		return fmt.Errorf("device-command: extend lease: %w", err)
	}
	return nil
}

// GetCommand fetches a command entry by ID (used by adapters after mutation).
func (s *Service) GetCommand(ctx context.Context, cmdID string) (command.Entry, error) {
	e, err := s.queue.GetCommand(ctx, cmdID)
	if err != nil {
		return command.Entry{}, fmt.Errorf("device-command: get command: %w", err)
	}
	return *e, nil
}

// ScanActive returns a paginated read-only view of non-terminal commands for
// ops/internal endpoints. It never claims commands or mutates state.
func (s *Service) ScanActive(
	ctx context.Context, filter command.ScanFilter, pageReq query.PageParams,
) (query.PageResult[command.Entry], error) {
	if filter.DeviceID != "" {
		if _, err := s.deviceRepo.GetByID(ctx, filter.DeviceID); err != nil {
			return query.PageResult[command.Entry]{}, fmt.Errorf(errLookupDeviceFmt, err)
		}
	}
	sliceName := s.sliceName
	if sliceName == "" {
		sliceName = "device-command"
	}
	qctx := query.QueryContext(
		"endpoint", sliceName+"-active",
		"deviceId", filter.DeviceID,
		"statuses", formatStatuses(filter.Statuses),
	)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[command.Entry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       pendingSort,
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]command.Entry, error) {
			// Load matching non-terminal entries, then apply in-memory cursor
			// pagination. For large-scale backends, a native SQL cursor is preferred.
			all, err := s.queue.ScanActive(ctx, filter)
			if err != nil {
				return nil, fmt.Errorf("device-command: scan active: %w", err)
			}
			query.Sort(all, params.Sort, entryFieldCompare)
			return query.ApplyCursor(all, params, entryFieldValue)
		},
		Extract: func(e command.Entry) []any {
			return []any{e.CreatedAt.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, sliceName),
		RunMode:     s.runMode,
	})
}

func formatStatuses(statuses []command.Status) string {
	if len(statuses) == 0 {
		return ""
	}
	parts := make([]string, 0, len(statuses))
	for _, status := range statuses {
		parts = append(parts, status.String())
	}
	return strings.Join(parts, ",")
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

// ParseAckReason parses a string ack reason into a command.AckReason value.
// Exported so slice adapters can call it without duplicating the switch.
func ParseAckReason(raw string) (command.AckReason, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "success":
		return command.AckSuccess, nil
	case "failure":
		return command.AckFailed, nil
	case "rejected":
		return command.AckRejected, nil
	default:
		return 0, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "devicecommand: invalid ack reason")
	}
}

// ParseStatusFilter parses the comma-separated status query parameter.
// Exported so slice adapters can call it without duplicating the switch.
func ParseStatusFilter(raw string) ([]command.Status, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	statuses := make([]command.Status, 0, len(parts))
	for _, part := range parts {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "", "all":
			continue
		case "pending":
			statuses = append(statuses, command.StatusPending)
		case "sent":
			statuses = append(statuses, command.StatusSent)
		case "delivered":
			statuses = append(statuses, command.StatusDelivered)
		default:
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "devicecommand: invalid status filter")
		}
	}
	return statuses, nil
}

func (s *Service) getOwnedCommand(ctx context.Context, deviceID, cmdID string) error {
	e, err := s.queue.GetCommand(ctx, cmdID)
	if err != nil {
		return fmt.Errorf("device-command: get command: %w", err)
	}

	if e.DeviceID != deviceID {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"device-command: command does not belong to this device",
			errcode.WithInternal(fmt.Sprintf("command %q does not belong to device %q", cmdID, deviceID)))
	}
	return nil
}
