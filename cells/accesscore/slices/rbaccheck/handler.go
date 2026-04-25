package rbaccheck

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract spec literals — cross-checked against
// contracts/http/auth/role/{list,check}/v1/contract.yaml by FMT-18.
var (
	specRoleList = wrapper.ContractSpec{
		ID: "http.auth.role.list.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/access/roles/{userID}",
	}
	specRoleCheck = wrapper.ContractSpec{
		ID: "http.auth.role.check.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/access/roles/{userID}/{roleName}",
	}
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

// RegisterRoutes registers rbac-check routes on the given mux via auth.Mount
// so every request emits a contract-tagged span. Policy is declared at
// registration time; handler bodies contain only business logic.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	auth.Mount(mux, auth.Route{
		Contract: specRoleList,
		Handler:  http.HandlerFunc(h.handleListRoles),
		Policy:   auth.SelfOr("userID", "admin"),
	})
	auth.Mount(mux, auth.Route{
		Contract: specRoleCheck,
		Handler:  http.HandlerFunc(h.handleHasRole),
		Policy:   auth.SelfOr("userID", "admin"),
	})
}

func (h *Handler) handleListRoles(w http.ResponseWriter, r *http.Request) {
	userID := r.PathValue("userID")

	pageReq, ok := httputil.ParsePageParamsOrWrite(w, r)
	if !ok {
		return
	}

	result, err := h.svc.ListRoles(r.Context(), userID, pageReq)
	if err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, query.MapPageResult(result, toRoleResponse))
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
