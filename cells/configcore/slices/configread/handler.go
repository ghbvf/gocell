package configread

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// spec vars for configread routes, cross-checked against
// contracts/http/config/{get,list}/v1/contract.yaml by FMT-18.
var (
	specConfigList = wrapper.ContractSpec{
		ID: "http.config.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/config/",
	}
	specConfigGet = wrapper.ContractSpec{
		ID: "http.config.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/config/{key}",
	}
)

// Handler provides HTTP endpoints for config read operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-read Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers config-read routes on mux via auth.Mount so
// CH-04/CH-05 governance can correlate contracts to handler functions.
// Both routes are admin-gated (auth.AnyRole(RoleAdmin)).
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specConfigList,
		Handler:  http.HandlerFunc(h.HandleList),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
	auth.Mount(mux, auth.Route{
		Contract: specConfigGet,
		Handler:  http.HandlerFunc(h.HandleGet),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
}

// HandleGet handles GET /{key} — returns a single config entry.
func (h *Handler) HandleGet(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	entry, err := h.svc.GetByKey(r.Context(), key)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
}

// HandleList handles GET /?limit=N&cursor=TOKEN — returns paginated config entries.
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

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, dto.ToConfigEntryResponse))
}
