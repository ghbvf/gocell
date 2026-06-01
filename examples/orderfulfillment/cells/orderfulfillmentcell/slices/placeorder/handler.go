package placeorder

import (
	"context"
	"errors"

	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time assertion: Handler implements the generated Service interface.
var _ placeordergen.Service = (*Handler)(nil)

// Handler is the typed-response adapter bridging the generated
// placeorder.Service interface to the domain Service.
type Handler struct {
	svc *Service
}

// NewHandler creates a Handler backed by the given Service.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// Placeorder implements placeordergen.Service.
//
// Response semantics:
//   - 202 Accepted: order placed (new) or idempotent hit (same key, same parameters).
//   - 409 Conflict: idempotency key reused with different parameters (item,
//     amountCents, or paymentShouldFail differ from the original request).
//     The caller must use a distinct key for a new order with different parameters.
//   - (nil, err): unhandled infrastructure faults — framework writes 5xx via
//     httputil.WriteError.
func (h *Handler) Placeorder(ctx context.Context, req *placeordergen.Request) (placeordergen.PlaceorderResponseObject, error) {
	psf := req.PaymentShouldFail != nil && *req.PaymentShouldFail
	id, err := h.svc.PlaceOrder(ctx, req.IdempotencyKey, req.Item, req.AmountCents, psf)
	if err != nil {
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Kind == errcode.KindConflict {
			// Idempotency key reuse with different parameters → typed 409.
			return placeordergen.Placeorder409ErrorResponse{Body: *ec}, nil
		}
		// Unhandled infrastructure fault → framework 5xx fallback.
		return nil, err
	}
	return placeordergen.Placeorder202JSONResponse{
		Data: &placeordergen.ResponseData{
			OrderID: id,
			Status:  "accepted",
		},
	}, nil
}
