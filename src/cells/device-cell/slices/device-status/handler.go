package devicestatus

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for device status queries.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-status Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleGetStatus handles GET /api/v1/devices/{id}/status.
func (h *Handler) HandleGetStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	device, err := h.svc.GetStatus(r.Context(), id)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"id":       device.ID,
			"name":     device.Name,
			"status":   device.Status,
			"lastSeen": device.LastSeen,
		},
	})
}
