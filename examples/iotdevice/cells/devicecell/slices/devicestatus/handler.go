package devicestatus

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// DeviceStatusResponse is the public DTO for device status queries.
type DeviceStatusResponse struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

func toDeviceStatusResponse(d *domain.Device) DeviceStatusResponse {
	if d == nil {
		return DeviceStatusResponse{}
	}
	return DeviceStatusResponse{
		ID:       d.ID,
		Name:     d.Name,
		Status:   d.Status,
		LastSeen: d.LastSeen,
	}
}

// specDeviceStatus declares the contract for the device-status endpoint.
// FMT-18 exempts examples/**, but keeping the ID aligned with
// examples/iotdevice/contracts/http/device/status/v1/contract.yaml.
var specDeviceStatus = wrapper.ContractSpec{
	ID: "http.device.status.v1", Kind: "http", Transport: "http",
	Method: "GET", Path: "/api/v1/devices/{id}/status",
}

// Handler provides HTTP endpoints for device status queries.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-status Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers the device-status route on mux via auth.Mount so
// CH-04/CH-05 governance can correlate this contract to HandleGetStatus.
// Device status is queried by operators or by the device itself.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.MustMount(mux, auth.Route{
		Contract: specDeviceStatus,
		Handler:  http.HandlerFunc(h.HandleGetStatus),
		Policy:   auth.AnyRole(dto.RoleOperator, dto.RoleDevice),
	})
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
