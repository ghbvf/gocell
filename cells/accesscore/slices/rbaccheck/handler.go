package rbaccheck

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	checkg "github.com/ghbvf/gocell/generated/contracts/http/auth/role/check/v1"
	listg "github.com/ghbvf/gocell/generated/contracts/http/auth/role/list/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// RoleResponse is the public DTO for Role, isolating the API contract from the
// domain model. Kept for unit tests that reference toRoleResponse directly.
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

// HasRoleResponse is the public DTO for role-check results. Kept for unit tests.
type HasRoleResponse struct {
	HasRole bool `json:"hasRole"`
}

// ListAdapter implements listg.Service for http.auth.role.list.v1.
type ListAdapter struct{ S *Service }

// List implements listg.Service. The generated handler already validates and
// decodes userID (UUID), cursor, and limit from the request.
func (a ListAdapter) List(ctx context.Context, req *listg.Request) (*listg.Response, error) {
	pageReq := query.PageParams{
		Cursor: req.Cursor,
		Limit:  int(req.Limit),
	}
	result, err := a.S.ListRoles(ctx, req.UserID, pageReq)
	if err != nil {
		return nil, err
	}
	items := make([]*listg.ResponseDataItem, 0, len(result.Items))
	for _, role := range result.Items {
		perms := make([]*listg.ResponseDataItemPermissionsItem, len(role.Permissions))
		for i, p := range role.Permissions {
			perms[i] = &listg.ResponseDataItemPermissionsItem{
				Resource: p.Resource,
				Action:   p.Action,
			}
		}
		items = append(items, &listg.ResponseDataItem{
			ID:          role.ID,
			Name:        role.Name,
			Permissions: perms,
		})
	}
	return &listg.Response{
		Data:       items,
		NextCursor: result.NextCursor,
		HasMore:    result.HasMore,
	}, nil
}

// CheckAdapter implements checkg.Service for http.auth.role.check.v1.
type CheckAdapter struct{ S *Service }

// Check implements checkg.Service. The generated handler already validates and
// decodes userID (UUID) and roleName from the request.
func (a CheckAdapter) Check(ctx context.Context, req *checkg.Request) (*checkg.Response, error) {
	has, err := a.S.HasRole(ctx, req.UserID, req.RoleName)
	if err != nil {
		return nil, err
	}
	return &checkg.Response{Data: &checkg.ResponseData{HasRole: has}}, nil
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
func (h *Handler) RegisterRoutes(mux kcell.RouteMux) error {
	if err := h.listH.RegisterRoutes(mux); err != nil {
		return err
	}
	return h.checkH.RegisterRoutes(mux)
}
