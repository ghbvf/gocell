package devicecommand

import (
	"context"
	"errors"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	kcell "github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	ackcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/ack/v1"
	dequeuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/dequeue/v1"
	enqueueasynccontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue-async/v1"
	enqueuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue/v1"
	extendleasecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/extend-lease/v1"
	reportcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/report/v1"
)

// EnqueueAdapter wraps Service to implement enqueuecontract.Service.
// It bridges the generated contract interface to the domain Service method,
// keeping the trust boundary at the adapter layer.
type EnqueueAdapter struct{ S *Service }

// Enqueue implements enqueuecontract.Service.
func (a EnqueueAdapter) Enqueue(ctx context.Context, req *enqueuecontract.Request) (enqueuecontract.EnqueueResponseObject, error) {
	entry, err := a.S.Enqueue(ctx, req.ID, req.CommandType, req.Payload)
	if err != nil {
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrRateLimited {
			return enqueuecontract.Enqueue429ErrorResponse{Body: *ec}, nil
		}
		return nil, err
	}
	return enqueuecontract.Enqueue201JSONResponse{Data: toEnqueueResponseData(entry)}, nil
}

// EnqueueAsyncAdapter wraps Service to implement enqueueasynccontract.Service.
// Unlike EnqueueAdapter (synchronous Pending write, 201), it emits the command
// through the outbox so the relay's Claimer wrap deduplicates the dispatch by
// DeriveCommandKey across cells/listeners/pods (the #1610 cross-cell consumer),
// returning 202 Accepted. The command_id is sourced from the request's
// Idempotency-Key in ctx by the bridge; a missing key fail-closes as a 400.
type EnqueueAsyncAdapter struct{ S *Service }

// EnqueueAsync implements enqueueasynccontract.Service.
func (a EnqueueAsyncAdapter) EnqueueAsync(
	ctx context.Context, req *enqueueasynccontract.Request,
) (enqueueasynccontract.EnqueueAsyncResponseObject, error) {
	if err := a.S.EnqueueAsync(ctx, req.ID, req.CommandType, req.Payload); err != nil {
		return nil, err
	}
	return enqueueasynccontract.EnqueueAsync202JSONResponse{Data: &enqueueasynccontract.ResponseData{
		DeviceID:    req.ID,
		CommandType: req.CommandType,
		Status:      "accepted",
	}}, nil
}

// DequeueAdapter wraps Service to implement dequeuecontract.Service.
type DequeueAdapter struct{ S *Service }

// Dequeue implements dequeuecontract.Service.
func (a DequeueAdapter) Dequeue(ctx context.Context, req *dequeuecontract.Request) (dequeuecontract.DequeueResponseObject, error) {
	entries, err := a.S.Dequeue(ctx, req.ID, int(req.Limit), command.DefaultLeaseDuration)
	if err != nil {
		return nil, err
	}
	items := make([]*dequeuecontract.ResponseDataItem, 0, len(entries))
	for _, e := range entries {
		items = append(items, toDequeueResponseDataItem(e))
	}
	return dequeuecontract.Dequeue200JSONResponse{Data: items, NextCursor: "", HasMore: false}, nil
}

// ReportAdapter wraps Service to implement reportcontract.Service.
type ReportAdapter struct{ S *Service }

// Report implements reportcontract.Service.
func (a ReportAdapter) Report(ctx context.Context, req *reportcontract.Request) (reportcontract.ReportResponseObject, error) {
	if err := a.S.Report(ctx, req.ID, req.CmdID); err != nil {
		return nil, err
	}
	entry, err := a.S.GetCommand(ctx, req.CmdID)
	if err != nil {
		return nil, err
	}
	return reportcontract.Report200JSONResponse{Data: toReportResponseData(entry)}, nil
}

// AckAdapter wraps Service to implement ackcontract.Service.
type AckAdapter struct{ S *Service }

// Ack implements ackcontract.Service.
func (a AckAdapter) Ack(ctx context.Context, req *ackcontract.Request) (ackcontract.AckResponseObject, error) {
	reason, err := devicecmd.ParseAckReason(req.Reason)
	if err != nil {
		return nil, err
	}
	if err := a.S.Ack(ctx, req.ID, req.CmdID, reason); err != nil {
		return nil, err
	}
	entry, err := a.S.GetCommand(ctx, req.CmdID)
	if err != nil {
		return nil, err
	}
	return ackcontract.Ack200JSONResponse{Data: toAckResponseData(entry)}, nil
}

// ExtendLeaseAdapter wraps Service to implement extendleasecontract.Service.
type ExtendLeaseAdapter struct{ S *Service }

// ExtendLease implements extendleasecontract.Service.
func (a ExtendLeaseAdapter) ExtendLease(
	ctx context.Context, req *extendleasecontract.Request,
) (extendleasecontract.ExtendLeaseResponseObject, error) {
	if err := a.S.ExtendLease(ctx, req.ID, req.CmdID, time.Duration(req.ExtensionSeconds)*time.Second); err != nil {
		return nil, err
	}
	entry, err := a.S.GetCommand(ctx, req.CmdID)
	if err != nil {
		return nil, err
	}
	return extendleasecontract.ExtendLease200JSONResponse{Data: toExtendLeaseResponseData(entry)}, nil
}

// Handler is the composite route handler for the public devicecommand slice.
// It holds the six generated per-contract handlers and exposes RegisterRoutes
// (primary listener: enqueue + enqueue-async + dequeue + report + ack +
// extend-lease). The internal control-plane list is owned by the sibling
// devicecommandinternal slice.
type Handler struct {
	enqueueH      *enqueuecontract.Handler
	enqueueAsyncH *enqueueasynccontract.Handler
	dequeueH      *dequeuecontract.Handler
	reportH       *reportcontract.Handler
	ackH          *ackcontract.Handler
	extendLeaseH  *extendleasecontract.Handler
}

// NewHandler creates a public devicecommand Handler with generated per-contract
// handlers. Authorization is contract-derived (#2486): each generated handler derives
// its gate from contract.yaml endpoints.http.{permission,resource} via the single
// RequirePermissionForContract funnel + the cell-level authz.MethodPolicyResolver
// (cellgen-built, injected from cell_init). No hand-wired gates here.
//   - enqueue / enqueue-async: coarse device:command — admin or operator PDP baseline.
//   - dequeue/report/ack/extend-lease: owner-scoped (resource: id) device:consume —
//     the device itself (subject==resource via PDP ownership rule) or admin/operator.
func NewHandler(svc *Service, resolver authz.MethodPolicyResolver) *Handler {
	return &Handler{
		enqueueH:      enqueuecontract.NewHandler(EnqueueAdapter{svc}, resolver),
		enqueueAsyncH: enqueueasynccontract.NewHandler(EnqueueAsyncAdapter{svc}, resolver),
		dequeueH:      dequeuecontract.NewHandler(DequeueAdapter{svc}, resolver),
		reportH:       reportcontract.NewHandler(ReportAdapter{svc}, resolver),
		ackH:          ackcontract.NewHandler(AckAdapter{svc}, resolver),
		extendLeaseH:  extendleasecontract.NewHandler(ExtendLeaseAdapter{svc}, resolver),
	}
}

// RegisterRoutes mounts the six public device-command routes on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.enqueueH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.enqueueAsyncH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.dequeueH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.reportH.RegisterRoutes(mux); err != nil {
		return err
	}
	if err := h.ackH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.extendLeaseH.RegisterRoutes(mux)
}

// ─── response converters ───────────────────────────────────────────────────

// commandEntryFields is the canonical projection of command.Entry into the
// flat string/int64 shape shared by every contract response DTO in this slice.
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
		ID: f.ID, DeviceID: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toDequeueResponseDataItem(e command.Entry) *dequeuecontract.ResponseDataItem {
	f := entryToFields(e)
	return &dequeuecontract.ResponseDataItem{
		ID: f.ID, DeviceID: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toReportResponseData(e command.Entry) *reportcontract.ResponseData {
	f := entryToFields(e)
	return &reportcontract.ResponseData{
		ID: f.ID, DeviceID: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toAckResponseData(e command.Entry) *ackcontract.ResponseData {
	f := entryToFields(e)
	return &ackcontract.ResponseData{
		ID: f.ID, DeviceID: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}

func toExtendLeaseResponseData(e command.Entry) *extendleasecontract.ResponseData {
	f := entryToFields(e)
	return &extendleasecontract.ResponseData{
		ID: f.ID, DeviceID: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}
