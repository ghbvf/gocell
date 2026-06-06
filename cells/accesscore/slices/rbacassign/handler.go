package rbacassign

import (
	"context"

	assign "github.com/ghbvf/gocell/generated/contracts/http/auth/role/assign/v1"
	revoke "github.com/ghbvf/gocell/generated/contracts/http/auth/role/revoke/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// AssignAdapter implements assign.Service for http.auth.role.assign.v1.
type AssignAdapter struct{ S *Service }

// Assign implements assign.Service.
// tenantID is parsed from req.TenantID (#1337 PR-3b): the InternalListener
// service-token caller has no JWT, so the tenant is supplied in the request body.
func (a AssignAdapter) Assign(ctx context.Context, req *assign.Request) (assign.AssignResponseObject, error) {
	tid, err := tenant.ParseTenantID(req.TenantID)
	if err != nil {
		resp400 := assign.Assign400ErrorResponse{Body: *errcode.New(
			errcode.KindInvalid, errcode.ErrAuthRBACInvalidInput, "invalid tenantId",
			errcode.WithDetails(errcode.PublicString("tenantId", req.TenantID)),
		)}
		return resp400, nil //nolint:nilerr // typed-response-envelope: declared 400 returned as typed struct + nil err (cell-patterns.md)
	}
	if err := a.S.Assign(ctx, tid, req.UserID, req.RoleID); err != nil {
		return nil, err
	}
	return assign.Assign201JSONResponse{Data: &assign.ResponseData{
		UserID:   req.UserID,
		RoleID:   req.RoleID,
		Assigned: true,
	}}, nil
}

// RevokeAdapter implements revoke.Service for http.auth.role.revoke.v1.
type RevokeAdapter struct{ S *Service }

// Revoke implements revoke.Service.
// tenantID is parsed from req.TenantID (#1337 PR-3b): same rationale as Assign.
func (a RevokeAdapter) Revoke(ctx context.Context, req *revoke.Request) (revoke.RevokeResponseObject, error) {
	tid, err := tenant.ParseTenantID(req.TenantID)
	if err != nil {
		resp400 := revoke.Revoke400ErrorResponse{Body: *errcode.New(
			errcode.KindInvalid, errcode.ErrAuthRBACInvalidInput, "invalid tenantId",
			errcode.WithDetails(errcode.PublicString("tenantId", req.TenantID)),
		)}
		return resp400, nil //nolint:nilerr // typed-response-envelope: declared 400 returned as typed struct + nil err (cell-patterns.md)
	}
	if err := a.S.Revoke(ctx, tid, req.UserID, req.RoleID); err != nil {
		return nil, err
	}
	return revoke.Revoke200JSONResponse{Data: &revoke.ResponseData{
		UserID:  req.UserID,
		RoleID:  req.RoleID,
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
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.assignH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.revokeH.RegisterRoutes(mux)
}
