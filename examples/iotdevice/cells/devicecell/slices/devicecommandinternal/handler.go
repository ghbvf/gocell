package devicecommandinternal

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/internalapi/devicecommands/list/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/pkg/query"
)

// InternalListAdapter wraps Service to implement listcontract.Service for
// http.internal.devicecommands.list.v1. Same scan logic as the public
// devicecommand slice's ScanActive; mounted on the InternalListener where
// service-token + caller-cell auth is enforced by the listener chain.
type InternalListAdapter struct{ S *Service }

// List implements listcontract.Service.
func (a InternalListAdapter) List(ctx context.Context, req *listcontract.Request) (listcontract.ListResponseObject, error) {
	statuses, err := devicecmd.ParseStatusFilter(req.Statuses)
	if err != nil {
		return nil, err
	}
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := a.S.ScanActive(ctx, command.ScanFilter{
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
	return listcontract.List200JSONResponse{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// Handler is the route handler for the internal devicecommandinternal slice.
// It holds the internal list generated handler and exposes RegisterRoutes;
// the cell mounts it on the InternalListener (see cell.go marker).
type Handler struct {
	internalListH *listcontract.Handler
}

// NewHandler creates an internal devicecommandinternal Handler.
// The single-argument NewHandler relies on the caller-cell allowlist injected
// by auth.Mount from contractSpec.Clients (devicecell → RequireCallerCell).
func NewHandler(svc *Service) *Handler {
	return &Handler{
		internalListH: listcontract.NewHandler(InternalListAdapter{svc}),
	}
}

// RegisterRoutes mounts the internal control-plane list on mux. The cell wires
// this onto the InternalListener via the +slice:route marker in cell.go.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	return h.internalListH.RegisterRoutes(mux)
}

// toListResponseDataItem converts a command.Entry to listcontract.ResponseDataItem.
func toListResponseDataItem(e command.Entry) *listcontract.ResponseDataItem {
	f := listcontract.ResponseDataItem{
		ID:          e.ID,
		DeviceId:    e.DeviceID,
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
	return &f
}
