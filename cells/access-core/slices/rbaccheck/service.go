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
)

// Service implements RBAC query operations.
type Service struct {
	roleRepo ports.RoleRepository
	logger   *slog.Logger
}

// NewService creates an rbac-check Service.
func NewService(roleRepo ports.RoleRepository, logger *slog.Logger) *Service {
	return &Service{roleRepo: roleRepo, logger: logger}
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

// ListRoles returns all roles assigned to a user.
func (s *Service) ListRoles(ctx context.Context, userID string) ([]*domain.Role, error) {
	if userID == "" {
		return nil, errcode.New(errcode.ErrAuthRBACInvalidInput, "userID is required")
	}

	roles, err := s.roleRepo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("rbac-check: list roles: %w", err)
	}
	return roles, nil
}
