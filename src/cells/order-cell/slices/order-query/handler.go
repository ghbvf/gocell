package orderquery

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for order queries.
type Handler struct {
	svc *Service
}

// NewHandler creates an order-query Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleGet handles GET /api/v1/orders/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	order, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": order})
}

// HandleList handles GET /api/v1/orders.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	orders, err := h.svc.List(r.Context())
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data":  orders,
		"total": len(orders),
	})
}
