package orderquery

import (
	"net/http"
	"time"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// OrderResponse is the public DTO for Order, isolating the API contract from
// the domain model.
type OrderResponse struct {
	ID        string    `json:"id"`
	Item      string    `json:"item"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

func toOrderResponse(o *domain.Order) OrderResponse {
	if o == nil {
		return OrderResponse{}
	}
	return OrderResponse{
		ID: o.ID, Item: o.Item, Status: o.Status, CreatedAt: o.CreatedAt,
	}
}

// spec vars for orderquery routes. FMT-18 exempts examples/**, but keeping IDs
// aligned with examples/todoorder/contracts/http/order/*/v1/contract.yaml.
var (
	specOrderGet = wrapper.ContractSpec{
		ID: "http.order.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/orders/{id}",
	}
	specOrderList = wrapper.ContractSpec{
		ID: "http.order.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/orders/",
	}
)

// Handler provides HTTP endpoints for order queries.
type Handler struct {
	svc *Service
}

// NewHandler creates an order-query Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers order-query routes on mux via auth.Mount so
// CH-04/CH-05 governance can correlate contracts to handler functions.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.MustMount(mux, auth.Route{
		Contract: specOrderGet,
		Handler:  http.HandlerFunc(h.HandleGet),
		Policy:   auth.AnyRole(dto.RoleCustomer),
	})
	auth.MustMount(mux, auth.Route{
		Contract: specOrderList,
		Handler:  http.HandlerFunc(h.HandleList),
		Policy:   auth.AnyRole(dto.RoleCustomer),
	})
}

// HandleGet handles GET /api/v1/orders/{id}.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	order, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": toOrderResponse(order)})
}

// HandleList handles GET /api/v1/orders?limit=N&cursor=TOKEN.
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

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toOrderResponse))
}
