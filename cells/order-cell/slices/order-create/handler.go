package ordercreate

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/order-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// OrderCreateResponse is the public DTO for the order creation response.
type OrderCreateResponse struct {
	ID     string `json:"id"`
	Item   string `json:"item"`
	Status string `json:"status"`
}

func toOrderCreateResponse(o *domain.Order) OrderCreateResponse {
	return OrderCreateResponse{
		ID:     o.ID,
		Item:   o.Item,
		Status: o.Status,
	}
}

// Handler provides HTTP endpoints for order creation.
type Handler struct {
	svc *Service
}

// NewHandler creates an order-create Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// createRequest is the JSON body for POST /api/v1/orders.
type createRequest struct {
	Item string `json:"item"`
}

// HandleCreate handles POST /api/v1/orders.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	order, err := h.svc.Create(r.Context(), req.Item)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toOrderCreateResponse(order)})
}
