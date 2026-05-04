package deviceregister

import (
	"net/http"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// DeviceRegisterResponse is the public DTO for the device registration response.
type DeviceRegisterResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func toDeviceRegisterResponse(d *domain.Device) DeviceRegisterResponse {
	if d == nil {
		return DeviceRegisterResponse{}
	}
	return DeviceRegisterResponse{
		ID:     d.ID,
		Name:   d.Name,
		Status: d.Status,
	}
}

// specDeviceRegister declares the contract for the device-register endpoint.
// FMT-18 exempts examples/**, but keeping the ID aligned with
// examples/iotdevice/contracts/http/device/register/v1/contract.yaml.
var specDeviceRegister = wrapper.ContractSpec{
	ID: "http.device.register.v1", Kind: "http", Transport: "http",
	Method: "POST", Path: "/api/v1/devices",
}

// Handler provides HTTP endpoints for device registration.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-register Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers the device-register route on mux via auth.Mount so
// CH-04/CH-05 governance can correlate this contract to HandleRegister.
// Device registration is a public endpoint: devices bootstrap without a user JWT.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := auth.Mount(mux, auth.Route{
		Contract: specDeviceRegister,
		Handler:  http.HandlerFunc(h.HandleRegister),
		Public:   true,
	}); err != nil {
		return err
	}
	return nil
}

// registerRequest is the JSON body for POST /api/v1/devices.
type registerRequest struct {
	Name string `json:"name"`
}

// HandleRegister handles POST /api/v1/devices.
func (h *Handler) HandleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httputil.DecodeJSONStrict(r, &req, httputil.DefaultDecodeJSONLimit); err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}

	device, err := h.svc.Register(r.Context(), req.Name)
	if err != nil {
		httputil.WriteError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": toDeviceRegisterResponse(device)})
}
