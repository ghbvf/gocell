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
	"strings"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	ackcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/ack/v1"
	dequeuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/dequeue/v1"
	enqueuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue/v1"
	extendleasecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/extend-lease/v1"
	reportcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/report/v1"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/internalapi/devicecommands/list/v1"
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
type Service struct {
	queue      commandQueueStore
	deviceRepo domain.DeviceRepository
	codec      *query.CursorCodec
	logger     *slog.Logger
	runMode    query.RunMode
	clock      clock.Clock

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

// NewService creates a device-command Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
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

// generateID generates a random hex command ID.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("device-command: generate ID: %w", err)
	}
	return "cmd-" + hex.EncodeToString(b), nil
}

// enqueueInternal creates a new pending command for the given device.
//
// commandType defaults to "default" when empty — callers that don't specify
// a type (e.g. early demo scripts) get a sensible fallback without error.
// T3 DEVICE-ENQUEUE-RBAC: s.authz is called when non-nil to enforce RBAC.
// Authz is checked before device lookup to prevent timing-based information
// leakage (403 must precede 404 so callers cannot probe device existence).
// L4 consistency: Enqueue is a Pending write (no outbox required at this stage).
func (s *Service) enqueueInternal(ctx context.Context, deviceID, commandType, payload string) (command.Entry, error) {
	// Authz check before any data access — prevents 404 vs 403 timing probing.
	if s.authz != nil {
		if err := s.authz(ctx); err != nil {
			return command.Entry{}, errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
				"device-command: enqueue authorization failed", err)
		}
	}

	// Verify device exists.
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return command.Entry{}, fmt.Errorf("device-command: lookup device: %w", err)
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

// dequeueInternal claims pending commands for the given device and advances them to Sent.
// This is the poll endpoint used by devices in the L4 latent model.
func (s *Service) dequeueInternal(ctx context.Context, deviceID string, limit int, lease time.Duration) ([]command.Entry, error) {
	if _, err := s.deviceRepo.GetByID(ctx, deviceID); err != nil {
		return nil, fmt.Errorf("device-command: lookup device: %w", err)
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

// ScanActive returns a paginated read-only view of non-terminal commands for
// ops/internal endpoints. It never claims commands or mutates state.
func (s *Service) ScanActive(
	ctx context.Context, filter command.ScanFilter, pageReq query.PageParams,
) (query.PageResult[command.Entry], error) {
	if filter.DeviceID != "" {
		if _, err := s.deviceRepo.GetByID(ctx, filter.DeviceID); err != nil {
			return query.PageResult[command.Entry]{}, fmt.Errorf("device-command: lookup device: %w", err)
		}
	}
	qctx := query.QueryContext(
		"endpoint", "device-command-active",
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
		OnCursorErr: query.LogCursorError(s.logger, "device-command"),
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

// ─── generated-interface adapters ───────────────────────────────────────────

// commandEntryFields is the canonical projection of command.Entry into the
// flat string/int64 shape shared by every contract response DTO in this slice
// (enqueue / dequeue / report / ack / extend-lease / list). Generated DTO
// types are distinct (one per contract) so Go cannot share the converter; this
// helper centralizes the mapping so adding a field to command.Entry touches
// one place, not six.
//
// See docs/plans/202605011500-029-master-roadmap.md row 06.FU2
// (PR-V1-CONTRACT-RESPONSE-CONVERTER-CODEGEN) for the longer-term plan to
// generate these converters from response.schema.json + a declared source type.
type commandEntryFields struct {
	ID          string
	DeviceID    string
	CommandType string
	Payload     string
	Status      string
	Attempt     int64
	CreatedAt   string
	SentAt      string
	DeliveredAt string
	CompletedAt string
}

func entryToFields(e command.Entry) commandEntryFields {
	f := commandEntryFields{
		ID:          e.ID,
		DeviceID:    e.DeviceID,
		CommandType: e.CommandType,
		Payload:     string(e.Payload),
		Status:      e.Status.String(),
		Attempt:     int64(e.Attempt),
		CreatedAt:   e.CreatedAt.Format(time.RFC3339),
	}
	if e.SentAt != nil {
		f.SentAt = e.SentAt.Format(time.RFC3339)
	}
	if e.DeliveredAt != nil {
		f.DeliveredAt = e.DeliveredAt.Format(time.RFC3339)
	}
	if e.CompletedAt != nil {
		f.CompletedAt = e.CompletedAt.Format(time.RFC3339)
	}
	return f
}

func toEnqueueResponseData(e command.Entry) *enqueuecontract.ResponseData {
	f := entryToFields(e)
	return &enqueuecontract.ResponseData{
		ID: f.ID, DeviceId: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toDequeueResponseDataItem(e command.Entry) *dequeuecontract.ResponseDataItem {
	f := entryToFields(e)
	return &dequeuecontract.ResponseDataItem{
		ID: f.ID, DeviceId: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toReportResponseData(e command.Entry) *reportcontract.ResponseData {
	f := entryToFields(e)
	return &reportcontract.ResponseData{
		ID: f.ID, DeviceId: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toAckResponseData(e command.Entry) *ackcontract.ResponseData {
	f := entryToFields(e)
	return &ackcontract.ResponseData{
		ID: f.ID, DeviceId: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toExtendLeaseResponseData(e command.Entry) *extendleasecontract.ResponseData {
	f := entryToFields(e)
	return &extendleasecontract.ResponseData{
		ID: f.ID, DeviceId: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

// parseAckReason parses a string ack reason into a command.AckReason value.
// Moved from handler.go to service.go as part of the codegen migration (W3.2).
func parseAckReason(raw string) (command.AckReason, error) {
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

// getCommand is a shared helper for adapters that need to fetch the updated
// command after a mutation (Report, Ack, ExtendLease).
func (s *Service) getCommand(ctx context.Context, cmdID string) (command.Entry, error) {
	e, err := s.queue.GetCommand(ctx, cmdID)
	if err != nil {
		return command.Entry{}, fmt.Errorf("device-command: get command: %w", err)
	}
	return *e, nil
}

// Enqueue implements enqueuecontract.Service.
func (s *Service) Enqueue(ctx context.Context, req *enqueuecontract.Request) (*enqueuecontract.Response, error) {
	entry, err := s.enqueueInternal(ctx, req.ID, req.CommandType, req.Payload)
	if err != nil {
		return nil, err
	}
	return &enqueuecontract.Response{Data: toEnqueueResponseData(entry)}, nil
}

// Dequeue implements dequeuecontract.Service.
func (s *Service) Dequeue(ctx context.Context, req *dequeuecontract.Request) (*dequeuecontract.Response, error) {
	entries, err := s.dequeueInternal(ctx, req.ID, int(req.Limit), command.DefaultLeaseDuration)
	if err != nil {
		return nil, err
	}
	items := make([]*dequeuecontract.ResponseDataItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, toDequeueResponseDataItem(e))
	}
	return &dequeuecontract.Response{Data: items, NextCursor: "", HasMore: false}, nil
}

// Report implements reportcontract.Service.
func (s *Service) Report(ctx context.Context, req *reportcontract.Request) (*reportcontract.Response, error) {
	if err := s.reportInternal(ctx, req.ID, req.CmdId); err != nil {
		return nil, err
	}
	entry, err := s.getCommand(ctx, req.CmdId)
	if err != nil {
		return nil, err
	}
	return &reportcontract.Response{Data: toReportResponseData(entry)}, nil
}

// Ack implements ackcontract.Service.
func (s *Service) Ack(ctx context.Context, req *ackcontract.Request) (*ackcontract.Response, error) {
	reason, err := parseAckReason(req.Reason)
	if err != nil {
		return nil, err
	}
	if err := s.ackInternal(ctx, req.ID, req.CmdId, reason); err != nil {
		return nil, err
	}
	entry, err := s.getCommand(ctx, req.CmdId)
	if err != nil {
		return nil, err
	}
	return &ackcontract.Response{Data: toAckResponseData(entry)}, nil
}

// ExtendLease implements extendleasecontract.Service.
func (s *Service) ExtendLease(ctx context.Context, req *extendleasecontract.Request) (*extendleasecontract.Response, error) {
	if err := s.extendLeaseInternal(ctx, req.ID, req.CmdId, time.Duration(req.ExtensionSeconds)*time.Second); err != nil {
		return nil, err
	}
	entry, err := s.getCommand(ctx, req.CmdId)
	if err != nil {
		return nil, err
	}
	return &extendleasecontract.Response{Data: toExtendLeaseResponseData(entry)}, nil
}

// Compile-time interface checks.
var (
	_ enqueuecontract.Service     = (*Service)(nil)
	_ dequeuecontract.Service     = (*Service)(nil)
	_ reportcontract.Service      = (*Service)(nil)
	_ ackcontract.Service         = (*Service)(nil)
	_ extendleasecontract.Service = (*Service)(nil)
	_ listcontract.Service        = (*Service)(nil)
)

// reportInternal records that the device has received the command and started work.
func (s *Service) reportInternal(ctx context.Context, deviceID, cmdID string) error {
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

// ackInternal finalizes a command with the supplied terminal reason. Ack is a single
// Queue transition; it does not synthesize Sent/Delivered timestamps.
func (s *Service) ackInternal(ctx context.Context, deviceID, cmdID string, reason command.AckReason) error {
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

// extendLeaseInternal extends an existing command lease for a device that is still
// processing a command.
func (s *Service) extendLeaseInternal(ctx context.Context, deviceID, cmdID string, extension time.Duration) error {
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

func (s *Service) getOwnedCommand(ctx context.Context, deviceID, cmdID string) error {
	e, err := s.queue.GetCommand(ctx, cmdID)
	if err != nil {
		return fmt.Errorf("device-command: get command: %w", err)
	}

	if e.DeviceID != deviceID {
		return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			fmt.Sprintf("device-command: command %q does not belong to device %q", cmdID, deviceID))
	}
	return nil
}

// List implements listcontract.Service for the internal ops view.
// It calls ScanActive and converts the result to the generated Response shape.
func (s *Service) List(ctx context.Context, req *listcontract.Request) (*listcontract.Response, error) {
	statuses, err := parseStatusFilter(req.Statuses)
	if err != nil {
		return nil, err
	}
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := s.ScanActive(ctx, command.ScanFilter{
		DeviceID: req.DeviceId,
		Statuses: statuses,
	}, pageReq)
	if err != nil {
		return nil, err
	}
	items := make([]*listcontract.ResponseDataItem, 0, len(result.Items))
	for _, e := range result.Items {
		items = append(items, toListResponseDataItem(e))
	}
	return &listcontract.Response{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

func toListResponseDataItem(e command.Entry) *listcontract.ResponseDataItem {
	f := entryToFields(e)
	return &listcontract.ResponseDataItem{
		ID: f.ID, DeviceId: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

// parseStatusFilter parses the comma-separated status query parameter.
// Moved from internalhandler.go as part of the W3.0.5 codegen migration.
func parseStatusFilter(raw string) ([]command.Status, error) {
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
