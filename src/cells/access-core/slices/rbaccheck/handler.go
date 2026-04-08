package rbaccheck

import (
	"net/http"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// Handler provides HTTP endpoints for RBAC queries.
type Handler struct {
	svc *Service
}

// NewHandler creates an rbac-check Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers rbac-check routes on the given mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	mux.Handle("GET /{userID}", http.HandlerFunc(h.handleListRoles))
	mux.Handle("GET /{userID}/{roleName}", http.HandlerFunc(h.handleHasRole))
}

func (h *Handler) handleListRoles(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")

	roles, err := h.svc.ListRoles(r.Context(), userID)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": roles, "total": len(roles)})
}

func (h *Handler) handleHasRole(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	roleName := r.PathValue("roleName")

	has, err := h.svc.HasRole(r.Context(), userID, roleName)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"hasRole": has}})
}
