package placeorder

import (
	"context"

	placeordergen "github.com/ghbvf/gocell/generated/contracts/http/orderfulfillment/placeorder/v1"
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
// Returns Placeorder202JSONResponse on success.
// Returns (nil, err) for unhandled infrastructure faults (framework 5xx fallback).
func (h *Handler) Placeorder(ctx context.Context, req *placeordergen.Request) (placeordergen.PlaceorderResponseObject, error) {
	psf := req.PaymentShouldFail != nil && *req.PaymentShouldFail
	id, err := h.svc.PlaceOrder(ctx, req.Item, req.AmountCents, psf)
	if err != nil {
		// No declared business 4xx for this endpoint beyond the schema-level
		// 400/413 emitted by the generated handler. Pass infra errors through
		// so the framework writes a well-formed 5xx via httputil.WriteError.
		return nil, err
	}
	return placeordergen.Placeorder202JSONResponse{
		Data: &placeordergen.ResponseData{
			OrderID: id,
			Status:  "accepted",
		},
	}, nil
}
