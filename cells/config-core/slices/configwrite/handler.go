package configwrite

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/config-core/internal/dto"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
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
// cell.RouteHandler (satisfied by both *http.ServeMux and cell.RouteMux) so
// production wiring, contract tests, and cell-level integration tests share
// the same auth.Secured declarations.
func (h *Handler) RegisterRoutes(mux cell.RouteHandler) {
	mux.Handle("POST /", auth.Secured(h.HandleCreate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("PUT /{key}", auth.Secured(h.HandleUpdate, auth.AnyRole(dto.RoleAdmin)))
	mux.Handle("DELETE /{key}", auth.Secured(h.HandleDelete, auth.AnyRole(dto.RoleAdmin)))
}
