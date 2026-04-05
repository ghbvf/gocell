package rbaccheck

import (
	"net/http"

	"github.com/go-chi/chi/v5"

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

// Routes returns a chi.Router with rbac-check routes.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{userID}", h.handleListRoles)
	r.Get("/{userID}/{roleName}", h.handleHasRole)
	return r
}

func (h *Handler) handleListRoles(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")

	roles, err := h.svc.ListRoles(r.Context(), userID)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": roles, "total": len(roles)})
}

func (h *Handler) handleHasRole(w http.ResponseWriter, r *http.Request) {
	userID := chi.URLParam(r, "userID")
	roleName := chi.URLParam(r, "roleName")

	has, err := h.svc.HasRole(r.Context(), userID, roleName)
	if err != nil {
		httputil.WriteDomainError(w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"hasRole": has}})
}
