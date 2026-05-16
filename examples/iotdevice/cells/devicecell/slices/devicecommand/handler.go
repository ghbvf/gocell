package devicecommand

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	ackcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/ack/v1"
	dequeuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/dequeue/v1"
	enqueuecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/enqueue/v1"
	extendleasecontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/extend-lease/v1"
	reportcontract "github.com/ghbvf/gocell/generated/contracts/http/device/command/report/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/runtime/auth"
)

// EnqueueAdapter wraps Service to implement enqueuecontract.Service.
// It bridges the generated contract interface to the domain Service method,
// keeping the trust boundary at the adapter layer.
type EnqueueAdapter struct{ S *Service }

// Enqueue implements enqueuecontract.Service.
func (a EnqueueAdapter) Enqueue(ctx context.Context, req *enqueuecontract.Request) (enqueuecontract.EnqueueResponseObject, error) {
	entry, err := a.S.Enqueue(ctx, req.ID, req.CommandType, req.Payload)
	if err != nil {
		return nil, err
	}
	return enqueuecontract.Enqueue201JSONResponse{Data: toEnqueueResponseData(entry)}, nil
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
	if err := a.S.Report(ctx, req.ID, req.CmdId); err != nil {
		return nil, err
	}
	entry, err := a.S.GetCommand(ctx, req.CmdId)
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
	if err := a.S.Ack(ctx, req.ID, req.CmdId, reason); err != nil {
		return nil, err
	}
	entry, err := a.S.GetCommand(ctx, req.CmdId)
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
	if err := a.S.ExtendLease(ctx, req.ID, req.CmdId, time.Duration(req.ExtensionSeconds)*time.Second); err != nil {
		return nil, err
	}
	entry, err := a.S.GetCommand(ctx, req.CmdId)
	if err != nil {
		return nil, err
	}
	return extendleasecontract.ExtendLease200JSONResponse{Data: toExtendLeaseResponseData(entry)}, nil
}

// Handler is the composite route handler for the public devicecommand slice.
// It holds the five generated per-contract handlers and exposes RegisterRoutes
// (primary listener: enqueue + dequeue + report + ack + extend-lease).
// The internal control-plane list is owned by the sibling devicecommandinternal
// slice.
type Handler struct {
	enqueueH     *enqueuecontract.Handler
	dequeueH     *dequeuecontract.Handler
	reportH      *reportcontract.Handler
	ackH         *ackcontract.Handler
	extendLeaseH *extendleasecontract.Handler
}

// NewHandler creates a public devicecommand Handler with generated per-contract
// handlers. Policies mirror those previously set in cell.go:
//   - enqueue: admin or operator only
//   - dequeue/report/ack/extend-lease: self-or admin/operator (device polls its own commands)
func NewHandler(svc *Service) *Handler {
	selfOrAdminOp := auth.SelfOr("id", dto.RoleAdmin, dto.RoleOperator)
	return &Handler{
		enqueueH:     enqueuecontract.NewHandler(EnqueueAdapter{svc}, auth.AnyRole(dto.RoleAdmin, dto.RoleOperator)),
		dequeueH:     dequeuecontract.NewHandler(DequeueAdapter{svc}, selfOrAdminOp),
		reportH:      reportcontract.NewHandler(ReportAdapter{svc}, selfOrAdminOp),
		ackH:         ackcontract.NewHandler(AckAdapter{svc}, selfOrAdminOp),
		extendLeaseH: extendleasecontract.NewHandler(ExtendLeaseAdapter{svc}, selfOrAdminOp),
	}
}

// RegisterRoutes mounts the five public device-command routes on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.enqueueH.RegisterRoutes(mux); err != nil {
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
