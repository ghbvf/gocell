package devicestatus

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// DeviceStatusResponse is the public DTO for device status queries.
type DeviceStatusResponse struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

func toDeviceStatusResponse(d *domain.Device) DeviceStatusResponse {
	return DeviceStatusResponse{
		ID:       d.ID,
		Name:     d.Name,
		Status:   d.Status,
		LastSeen: d.LastSeen,
	}
}

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
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toDeviceStatusResponse(device)})
}
