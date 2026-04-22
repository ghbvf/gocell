// Package rbaccheck implements the rbac-check slice: HasRole / ListRoles
// queries for a given user.
package rbaccheck

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

var roleSort = []query.SortColumn{
	{Name: "name", Direction: query.SortASC},
	{Name: "id", Direction: query.SortASC},
}

// Service implements RBAC query operations.
type Service struct {
	roleRepo ports.RoleRepository
	codec    *query.CursorCodec
	logger   *slog.Logger
	runMode  query.RunMode
}

// NewService creates an rbac-check Service.
func NewService(roleRepo ports.RoleRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) *Service {
	return &Service{roleRepo: roleRepo, codec: codec, logger: logger, runMode: runMode}
}

// HasRole checks if a user has the specified role.
func (s *Service) HasRole(ctx context.Context, userID, roleName string) (bool, error) {
	if userID == "" || roleName == "" {
		return false, errcode.New(errcode.ErrAuthRBACInvalidInput, "userID and roleName are required")
	}

	roles, err := s.roleRepo.GetByUserID(ctx, userID)
	if err != nil {
		return false, fmt.Errorf("rbac-check: has role: %w", err)
	}

	for _, r := range roles {
		if r.Name == roleName {
			return true, nil
		}
	}
	return false, nil
}

// ListRoles returns a paginated page of roles assigned to userID.
func (s *Service) ListRoles(ctx context.Context, userID string, pageReq query.PageRequest) (query.PageResult[*domain.Role], error) {
	if userID == "" {
		return query.PageResult[*domain.Role]{}, errcode.New(errcode.ErrAuthRBACInvalidInput, "userID is required")
	}

	qctx := query.QueryContext("endpoint", "rbac-list-roles", "userId", userID)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.Role]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     roleSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.Role, error) {
			roles, err := s.roleRepo.ListByUserID(ctx, userID, params)
			if err != nil {
				return nil, fmt.Errorf("rbac-check: list roles: %w", err)
			}
			return roles, nil
		},
		Extract: func(r *domain.Role) []any {
			return []any{r.Name, r.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "rbac-check"),
		RunMode:     s.runMode,
	})
}
