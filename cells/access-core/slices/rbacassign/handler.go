package rbacassign

import (
	"net/http"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
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
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) {
	mux.Handle("POST /assign", http.HandlerFunc(h.handleAssign))
	mux.Handle("POST /revoke", http.HandlerFunc(h.handleRevoke))
}

func (h *Handler) handleAssign(w http.ResponseWriter, r *http.Request) {
	if !auth.Guard(w, r, auth.AnyRole(domain.RoleAdmin)) {
		return
	}

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
	if !auth.Guard(w, r, auth.AnyRole(domain.RoleAdmin)) {
		return
	}

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
