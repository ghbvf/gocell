package ordercreate

import (
	"encoding/json"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest,
			string(errcode.ErrValidationFailed), "invalid request body")
		return
	}

	order, err := h.svc.Create(r.Context(), req.Item)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":     order.ID,
		"item":   order.Item,
		"status": order.Status,
	})
}
