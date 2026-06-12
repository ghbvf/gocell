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
	commandruntime "github.com/ghbvf/gocell/runtime/command"
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
	queue      commandQueueStore       `gocell:"required"`
	deviceRepo domain.DeviceRepository `gocell:"required"`
	codec      *query.CursorCodec      `gocell:"required" gocellCode:"ErrCellMissingCodec" gocellErr:"device-command: cursor codec is required"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger     *slog.Logger
	runMode    query.RunMode
	clock      clock.Clock
	sliceName  string

	// authz is the optional T3 DEVICE-ENQUEUE-RBAC hook. Nil means no authz
	// check (demo mode). It is set by direct field assignment in test helpers
	// (e.g. svc.authz = rejectAll) or by the composition root; there is no
	// exported WithAuthz option.
	authz command.AuthzFunc

	// onResolved is an optional GENERIC command-resolution hook fired after a
	// terminal Ack (success/failure/rejected), carrying the resolved command
	// entry + the ack reason. It keeps devicecmd command-type agnostic: consumers
	// (e.g. the devicecertcompletion slice for rotate-cert, #1870) filter and
	// react. Nil means no hook (the common case). Set via WithOnCommandResolved by
	// the composition root. It is fire-and-forget — it must not fail the ack.
	onResolved func(context.Context, command.Entry, command.AckReason)
}

// Option configures a device-command Service.
type Option func(*Service)

// WithOnCommandResolved registers a generic hook fired after a command reaches a
// terminal status via Ack. The hook receives the resolved command entry and the
// ack reason; it is fire-and-forget (its errors are its own concern and never
// fail the device's ack). devicecmd stays command-type agnostic — the hook impl
// (the devicecertcompletion slice) does any command-type-specific filtering and
// reaction. A nil hook is ignored. Accumulative: a nil argument leaves the prior
// value in place.
func WithOnCommandResolved(hook func(context.Context, command.Entry, command.AckReason)) Option {
	return func(s *Service) {
		if hook != nil {
			s.onResolved = hook
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
	clk clock.Clock, q commandQueueStore, deviceRepo domain.DeviceRepository,
	codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "devicecmd.NewService")
	s := &Service{
		queue:      q,
		deviceRepo: deviceRepo,
		codec:      codec,
		logger:     logger,
		runMode:    runMode,
		clock:      clk,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
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

	// Read active-uniqueness (key, deadline) injected by the relay when it
	// dispatches a command that was emitted with command.WithActiveUniqueness.
	// ok=false for direct HTTP callers (no relay context) → today's behavior:
	// random id, no dedup, no deadline.
	opts := command.EnqueueOptions{Authz: s.authz}
	key, deadline, ok := commandruntime.DispatchedUniqueness(ctx)
	if ok {
		opts.IdempotencyKey = key
	}

	var timeouts command.Timeouts
	if ok {
		d := deadline.Sub(s.clock.Now())
		if d <= 0 {
			// Guard: deadline is in the past (clock skew or test drift). Use a
			// small positive floor so the command is never left un-sweepable
			// indefinitely — a command with no OverallDeadline would block the
			// active-uniqueness slot forever if the Sweeper never sees a deadline.
			d = time.Second
		}
		timeouts = command.Timeouts{OverallDeadline: d}
	}

	entry := command.NewEntry(id, deviceID, commandType, []byte(payload), timeouts, s.clock.Now())

	if err := s.queue.Enqueue(ctx, entry, opts); err != nil {
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
	if _, err := s.getOwnedCommand(ctx, deviceID, cmdID); err != nil {
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
	entry, err := s.getOwnedCommand(ctx, deviceID, cmdID)
	if err != nil {
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

	// Fire the generic command-resolution hook (#1870) after the terminal ack
	// has committed. The entry was fetched pre-ack (CommandType/Payload/DeviceID
	// do not change on the terminal transition). Fire-and-forget: a nil hook is
	// the common case, and the hook never fails the ack.
	if s.onResolved != nil {
		s.onResolved(ctx, entry, reason)
	}
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
	if _, err := s.getOwnedCommand(ctx, deviceID, cmdID); err != nil {
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

// getOwnedCommand fetches the command and verifies it belongs to deviceID. It
// returns the entry (by value) so callers that also need the command shape —
// e.g. Ack firing the onResolved hook (#1870) — avoid a second GetCommand.
func (s *Service) getOwnedCommand(ctx context.Context, deviceID, cmdID string) (command.Entry, error) {
	e, err := s.queue.GetCommand(ctx, cmdID)
	if err != nil {
		return command.Entry{}, fmt.Errorf("device-command: get command: %w", err)
	}

	if e.DeviceID != deviceID {
		return command.Entry{}, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"device-command: command does not belong to this device",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("command %q does not belong to device %q", cmdID, deviceID))))
	}
	return *e, nil
}
