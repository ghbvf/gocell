package rbaccheck

import (
	"context"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	checkg "github.com/ghbvf/gocell/generated/contracts/http/auth/role/check/v1"
	listg "github.com/ghbvf/gocell/generated/contracts/http/auth/role/list/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/projection"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// ListAdapter implements listg.Service for http.auth.role.list.v1.
type ListAdapter struct{ S *Service }

// List implements listg.Service. The generated handler already validates and
// decodes userID (UUID), cursor, and limit from the request.
func (a ListAdapter) List(ctx context.Context, req *listg.Request) (listg.ListResponseObject, error) {
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := a.S.ListRoles(ctx, req.UserID, pageReq)
	if err != nil {
		return nil, err
	}
	rows, err := toRoleProjectionRows(result.Items)
	if err != nil {
		return nil, err
	}
	return listg.List200JSONResponse{
		Data:       rows,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// toRoleProjectionRows converts domain roles into the sealed []projection.ResourceProjection
// required by the list response. Identity projection (epic #1337 PR-12); masking
// obligation source becomes the ABAC Decision in PR-10.
func toRoleProjectionRows(roles []*domain.Role) ([]projection.ResourceProjection, error) {
	rows := make([]map[string]any, 0, len(roles))
	for _, role := range roles {
		perms := make([]*listg.ResponseDataItemPermissionsItem, len(role.Permissions))
		for i, p := range role.Permissions {
			perms[i] = &listg.ResponseDataItemPermissionsItem{
				Resource: p.Resource,
				Action:   p.Action,
			}
		}
		item := listg.ResponseDataItem{
			ID:          role.ID,
			Name:        role.Name,
			Permissions: perms,
		}
		rows = append(rows, item.ToMap())
	}
	return projection.NewProjectionList(authz.FieldMask{}, rows)
}

// CheckAdapter implements checkg.Service for http.auth.role.check.v1.
type CheckAdapter struct{ S *Service }

// Check implements checkg.Service. The generated handler already validates and
// decodes userID (UUID) and roleName from the request.
func (a CheckAdapter) Check(ctx context.Context, req *checkg.Request) (checkg.CheckResponseObject, error) {
	has, err := a.S.HasRole(ctx, req.UserID, req.RoleName)
	if err != nil {
		return nil, err
	}
	// Identity projection (epic #1337 PR-12); masking obligation source becomes the ABAC Decision in PR-10.
	data, err := projection.NewProjection(authz.FieldMask{}, checkg.ResponseData{HasRole: has}.ToMap())
	if err != nil {
		return nil, err
	}
	return checkg.Check200JSONResponse{Data: data}, nil
}

// Handler is the composite route handler for the rbaccheck slice.
type Handler struct {
	listH  *listg.Handler
	checkH *checkg.Handler
}

// NewHandler creates an rbaccheck Handler with the generated list/check handlers.
func NewHandler(svc *Service) *Handler {
	policy := auth.SelfOr("userID", auth.RoleAdmin)
	return &Handler{
		listH:  listg.NewHandler(ListAdapter{svc}, policy),
		checkH: checkg.NewHandler(CheckAdapter{svc}, policy),
	}
}

// RegisterRoutes mounts the list and check contract handlers on mux.
func (h *Handler) RegisterRoutes(mux kcell.RouteHandler) error {
	if err := h.listH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.checkH.RegisterRoutes(mux)
}
