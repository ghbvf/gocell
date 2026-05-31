package orderstatus

import (
	"context"
	"errors"

	orderstatusgen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/orderstatus/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Handler adapts Service to the generated orderstatusgen.Service interface.
type Handler struct {
	svc *Service
}

// NewHandler creates a Handler bridging the generated contract to the domain Service.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Orderstatus implements orderstatusgen.Service.
// Returns 200 with the saga status on success; 404 when the order is not found.
func (h *Handler) Orderstatus(ctx context.Context, req *orderstatusgen.Request) (orderstatusgen.OrderstatusResponseObject, error) {
	status, err := h.svc.GetOrderStatus(ctx, req.ID)
	if err != nil {
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Kind == errcode.KindNotFound {
			return orderstatusgen.Orderstatus404ErrorResponse{Body: *ec}, nil
		}
		// Undeclared framework 5xx — let the generated handler call httputil.WriteError.
		return nil, err
	}
	return orderstatusgen.Orderstatus200JSONResponse(orderstatusgen.Response{
		Data: &orderstatusgen.ResponseData{
			OrderID: req.ID,
			Status:  status,
		},
	}), nil
}
