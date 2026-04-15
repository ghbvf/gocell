package deviceregister

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// DeviceRegisterResponse is the public DTO for the device registration response.
type DeviceRegisterResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func toDeviceRegisterResponse(d *domain.Device) DeviceRegisterResponse {
	return DeviceRegisterResponse{
		ID:     d.ID,
		Name:   d.Name,
		Status: d.Status,
	}
}

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
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	device, err := h.svc.Register(r.Context(), req.Name)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toDeviceRegisterResponse(device)})
}
