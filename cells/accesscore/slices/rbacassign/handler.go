package rbacassign

import (
	"net/http"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Contract spec literals — cross-checked against contracts/http/auth/role/
// {assign,revoke}/v1/contract.yaml by FMT-18.
var (
	specRoleAssign = wrapper.ContractSpec{
		ID: "http.auth.role.assign.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/internal/v1/access/roles/assign",
	}
	specRoleRevoke = wrapper.ContractSpec{
		ID: "http.auth.role.revoke.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/internal/v1/access/roles/revoke",
	}
)

// AssignRequest is the request DTO for role assignment.
type AssignRequest struct {
	UserID string `json:"userId"`
	RoleID string `json:"roleId"`
}

// AssignResponse is the response DTO for role assignment.
type AssignResponse struct {
	UserID   string `json:"userId"`
	RoleID   string `json:"roleId"`
	Assigned bool   `json:"assigned"`
}

// RevokeResponse is the response DTO for role revocation.
type RevokeResponse struct {
	UserID  string `json:"userId"`
	RoleID  string `json:"roleId"`
	Revoked bool   `json:"revoked"`
}

// Handler provides HTTP endpoints for RBAC role assignment/revocation.
type Handler struct {
	svc *Service
}

// NewHandler creates an rbac-assign Handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RevokeRequest is the request DTO for role revocation. Structurally identical
// to AssignRequest but kept as a separate type to allow schemas to evolve
// independently (e.g. future RevokeRequest might add `reason` or `effectiveAt`).
type RevokeRequest struct {
	UserID string `json:"userId"`
	RoleID string `json:"roleId"`
}

// RegisterRoutes registers rbac-assign routes on the given mux.
// Policy is declared at registration time; handler bodies contain only
// business logic (no inline guard calls).
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	internalAdminPolicy := auth.AnyRole(auth.RoleInternalAdmin)
	auth.MustMount(mux, auth.Route{
		Contract: specRoleAssign,
		Handler:  http.HandlerFunc(h.handleAssign),
		Policy:   internalAdminPolicy,
		// Route lives on InternalListener (/internal/v1/*); internal affinity
		// is derived from the path prefix via AuthRouteMeta.IsInternal().
	})
	auth.MustMount(mux, auth.Route{
		Contract: specRoleRevoke,
		Handler:  http.HandlerFunc(h.handleRevoke),
		Policy:   internalAdminPolicy,
	})
}

func (h *Handler) handleAssign(w http.ResponseWriter, r *http.Request) {
	var req AssignRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	if err := h.svc.Assign(r.Context(), req.UserID, req.RoleID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusCreated, map[string]any{
		"data": AssignResponse{
			UserID:   req.UserID,
			RoleID:   req.RoleID,
			Assigned: true,
		},
	})
}

func (h *Handler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	var req RevokeRequest
	if err := httputil.DecodeJSONStrict(r, &req); err != nil {
		httputil.WriteDecodeError(r.Context(), w, err)
		return
	}

	if err := h.svc.Revoke(r.Context(), req.UserID, req.RoleID); err != nil {
		httputil.WriteDomainError(r.Context(), w, err)
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]any{
		"data": RevokeResponse{
			UserID:  req.UserID,
			RoleID:  req.RoleID,
			Revoked: true,
		},
	})
}
