package rbacassign

import (
	"context"

	assign "github.com/ghbvf/gocell/generated/contracts/http/auth/role/assign/v1"
	revoke "github.com/ghbvf/gocell/generated/contracts/http/auth/role/revoke/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// AssignAdapter implements assign.Service for http.auth.role.assign.v1.
type AssignAdapter struct{ S *Service }

// Assign implements assign.Service.
func (a AssignAdapter) Assign(ctx context.Context, req *assign.Request) (assign.AssignResponseObject, error) {
	if err := a.S.Assign(ctx, req.UserId, req.RoleId); err != nil {
		return nil, err
	}
	return assign.Assign201JSONResponse{Data: &assign.ResponseData{
		UserId:   req.UserId,
		RoleId:   req.RoleId,
		Assigned: true,
	}}, nil
}

// RevokeAdapter implements revoke.Service for http.auth.role.revoke.v1.
type RevokeAdapter struct{ S *Service }

// Revoke implements revoke.Service.
func (a RevokeAdapter) Revoke(ctx context.Context, req *revoke.Request) (revoke.RevokeResponseObject, error) {
	if err := a.S.Revoke(ctx, req.UserId, req.RoleId); err != nil {
		return nil, err
	}
	return revoke.Revoke200JSONResponse{Data: &revoke.ResponseData{
		UserId:  req.UserId,
		RoleId:  req.RoleId,
		Revoked: true,
	}}, nil
}

// Handler is the composite route handler for the rbacassign slice.
// Both handlers explicitly pass auth.RequireCallerCell("accesscore") as
// defense-in-depth, complementing the auto-injected caller-cell guard from
// the generated contractSpec's Clients field.
type Handler struct {
	assignH *assign.Handler
	revokeH *revoke.Handler
}

// NewHandler creates an rbacassign Handler with the generated assign/revoke handlers.
func NewHandler(svc *Service) *Handler {
	callerPolicy := auth.RequireCallerCell("accesscore")
	return &Handler{
		assignH: assign.NewHandler(AssignAdapter{svc}, callerPolicy),
		revokeH: revoke.NewHandler(RevokeAdapter{svc}, callerPolicy),
	}
}

// RegisterRoutes mounts the assign and revoke contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) error {
	if err := h.assignH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.revokeH.RegisterRoutes(mux)
}
