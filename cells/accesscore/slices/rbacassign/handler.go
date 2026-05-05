package rbacassign

import (
	"context"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	assign "github.com/ghbvf/gocell/generated/contracts/http/auth/role/assign/v1"
	revoke "github.com/ghbvf/gocell/generated/contracts/http/auth/role/revoke/v1"
)

// AssignAdapter implements assign.Service for http.auth.role.assign.v1.
// Clients:["accesscore"] in the generated contractSpec means auth.Mount auto-applies
// RequireCallerCell; no explicit Policy needed.
type AssignAdapter struct{ S *Service }

// Assign implements assign.Service.
func (a AssignAdapter) Assign(ctx context.Context, req *assign.Request) (*assign.Response, error) {
	if err := a.S.Assign(ctx, req.UserId, req.RoleId); err != nil {
		return nil, err
	}
	return &assign.Response{Data: &assign.ResponseData{
		UserId:   req.UserId,
		RoleId:   req.RoleId,
		Assigned: true,
	}}, nil
}

// RevokeAdapter implements revoke.Service for http.auth.role.revoke.v1.
type RevokeAdapter struct{ S *Service }

// Revoke implements revoke.Service.
func (a RevokeAdapter) Revoke(ctx context.Context, req *revoke.Request) (*revoke.Response, error) {
	if err := a.S.Revoke(ctx, req.UserId, req.RoleId); err != nil {
		return nil, err
	}
	return &revoke.Response{Data: &revoke.ResponseData{
		UserId:  req.UserId,
		RoleId:  req.RoleId,
		Revoked: true,
	}}, nil
}

// Handler is the composite route handler for the rbacassign slice.
// The generated contractSpecs carry Clients:["accesscore"] so auth.Mount
// auto-injects RequireCallerCell — no explicit Policy argument needed.
type Handler struct {
	assignH *assign.Handler
	revokeH *revoke.Handler
}

// NewHandler creates an rbacassign Handler with the generated assign/revoke handlers.
func NewHandler(svc *Service) *Handler {
	return &Handler{
		assignH: assign.NewHandler(AssignAdapter{svc}, nil),
		revokeH: revoke.NewHandler(RevokeAdapter{svc}, nil),
	}
}

// RegisterRoutes mounts the assign and revoke contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) error {
	if err := h.assignH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.revokeH.RegisterRoutes(mux)
}
