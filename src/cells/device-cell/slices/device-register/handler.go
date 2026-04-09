package deviceregister

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for device registration.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-register Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// registerRequest is the JSON body for POST /api/v1/devices.
type registerRequest struct {
	Name string `json:"name"`
}

// HandleRegister handles POST /api/v1/devices.
func (h *Handler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httputil.DecodeJSON(r, &req); err != nil {
		httputil.WriteDecodeError(w, err)
		return
	}

	device, err := h.svc.Register(r.Context(), req.Name)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"id":     device.ID,
		"name":   device.Name,
		"status": device.Status,
	})
}
