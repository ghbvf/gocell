package configwrite

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract spec literals — cross-checked against
// contracts/http/config/{write,update,delete}/v1/contract.yaml by FMT-18.
var (
	specConfigWrite = wrapper.ContractSpec{
		ID: "http.config.write.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/api/v1/config/",
	}
	specConfigUpdate = wrapper.ContractSpec{
		ID: "http.config.update.v1", Kind: "http", Transport: "http",
		Method: "PUT", Path: "/api/v1/config/{key}",
	}
	specConfigDelete = wrapper.ContractSpec{
		ID: "http.config.delete.v1", Kind: "http", Transport: "http",
		Method: "DELETE", Path: "/api/v1/config/{key}",
	}
)

// Handler provides HTTP endpoints for config write operations.
type Handler struct {
	svc *Service
}

// NewHandler creates a config-write Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// HandleCreate handles POST / — creates a new config entry.
func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Key       string `json:"key"`
		Value     string `json:"value"`
		Sensitive bool   `json:"sensitive"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Create(r.Context(), CreateInput{Key: req.Key, Value: req.Value, Sensitive: req.Sensitive})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
}

// HandleUpdate handles PUT /{key} — updates an existing config entry.
func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	var req struct {
		Value string `json:"value"`
	}
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	entry, err := h.svc.Update(r.Context(), UpdateInput{Key: key, Value: req.Value})
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": dto.ToConfigEntryResponse(entry)})
}

// HandleDelete handles DELETE /{key} — deletes a config entry.
func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")

	if err := h.svc.Delete(r.Context(), key); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// RegisterRoutes registers configwrite routes with admin-only policies on any
// cell.RouteHandler (satisfied by *http.ServeMux, cell.RouteMux, and the chi
// sub-router adapter) so production wiring, contract tests, and cell-level
// integration tests share the same auth.Mount declarations.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) {
	auth.Mount(mux, auth.Route{
		Contract: specConfigWrite,
		Handler:  http.HandlerFunc(h.HandleCreate),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
	auth.Mount(mux, auth.Route{
		Contract: specConfigUpdate,
		Handler:  http.HandlerFunc(h.HandleUpdate),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
	auth.Mount(mux, auth.Route{
		Contract: specConfigDelete,
		Handler:  http.HandlerFunc(h.HandleDelete),
		Policy:   auth.AnyRole(dto.RoleAdmin),
	})
}
