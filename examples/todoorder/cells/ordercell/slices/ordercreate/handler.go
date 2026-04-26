package ordercreate

import (
	"net/http"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// OrderCreateResponse is the public DTO for the order creation response.
type OrderCreateResponse struct {
	ID     string `json:"id"`
	Item   string `json:"item"`
	Status string `json:"status"`
}

func toOrderCreateResponse(o *domain.Order) OrderCreateResponse {
	if o == nil {
		return OrderCreateResponse{}
	}
	return OrderCreateResponse{
		ID:     o.ID,
		Item:   o.Item,
		Status: o.Status,
	}
}

// specOrderCreate declares the contract for the order-create endpoint.
// FMT-18 exempts examples/**, but keeping the ID aligned with
// examples/todoorder/contracts/http/order/create/v1/contract.yaml.
var specOrderCreate = wrapper.ContractSpec{
	ID: "http.order.create.v1", Kind: "http", Transport: "http",
	Method: "POST", Path: "/api/v1/orders/",
}

// Handler provides HTTP endpoints for order creation.
type Handler struct {
	svc *Service
}

// NewHandler creates an order-create Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers the order-create route on mux via auth.Mount so
// CH-04/CH-05 governance can correlate this contract to HandleCreate.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specOrderCreate,
		Handler:  http.HandlerFunc(h.HandleCreate),
		Policy:   auth.AnyRole(dto.RoleCustomer),
	})
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
