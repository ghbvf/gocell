package devicelist

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/devicecell/internal/domain"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// DeviceResponse is the public DTO for Device.
type DeviceResponse struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"`
	LastSeen time.Time `json:"lastSeen"`
}

func toDeviceResponse(d *domain.Device) DeviceResponse {
	return DeviceResponse{
		ID:       d.ID,
		Name:     d.Name,
		Status:   d.Status,
		LastSeen: d.LastSeen,
	}
}

// Handler provides the GET /api/v1/devices/ endpoint.
type Handler struct {
	svc *Service
}

// NewHandler creates a device-list Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers device-list routes.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	auth.Declare(mux, auth.RouteDecl{
		Method:  "GET",
		Path:    "/",
		Handler: http.HandlerFunc(h.HandleList),
		Policy:  auth.AnyRole("admin"),
	})
}

// HandleList handles GET /api/v1/devices/.
func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
		return
	}

	result, err := h.svc.List(r.Context(), pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toDeviceResponse))
}
