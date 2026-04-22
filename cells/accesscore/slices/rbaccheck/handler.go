package rbaccheck

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// RoleResponse is the public DTO for Role, isolating the API contract from the
// domain model.
type RoleResponse struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Permissions []PermissionResponse `json:"permissions"`
}

// PermissionResponse is the public DTO for Permission.
type PermissionResponse struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

func toRoleResponse(r *domain.Role) RoleResponse {
	perms := make([]PermissionResponse, len(r.Permissions))
	for i, p := range r.Permissions {
		perms[i] = PermissionResponse{Resource: p.Resource, Action: p.Action}
	}
	return RoleResponse{ID: r.ID, Name: r.Name, Permissions: perms}
}

// HasRoleResponse is the public DTO for role-check results.
type HasRoleResponse struct {
	HasRole bool `json:"hasRole"`
}

// Handler provides HTTP endpoints for RBAC queries.
type Handler struct {
	svc *Service
}

// NewHandler creates an rbac-check Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes registers rbac-check routes on the given mux.
// Policy is declared at registration time via auth.Declare so that handler
// bodies contain only business logic (no inline guard calls).
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	auth.Declare(mux, auth.RouteDecl{
		Method:  "GET",
		Path:    "/{userID}",
		Handler: http.HandlerFunc(h.handleListRoles),
		Policy:  auth.SelfOr("userID", "admin"),
	})
	auth.Declare(mux, auth.RouteDecl{
		Method:  "GET",
		Path:    "/{userID}/{roleName}",
		Handler: http.HandlerFunc(h.handleHasRole),
		Policy:  auth.SelfOr("userID", "admin"),
	})
}

func (h *Handler) handleListRoles(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	roles, err := h.svc.ListRoles(r.Context(), userID)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	resp := make([]RoleResponse, len(roles))
	for i, role := range roles {
		resp[i] = toRoleResponse(role)
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": resp, "hasMore": false})
}

func (h *Handler) handleHasRole(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")
	roleName := r.PathValue("roleName")

	has, err := h.svc.HasRole(r.Context(), userID, roleName)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{"data": HasRoleResponse{HasRole: has}})
}
