// Package authorizationdecide implements the authorization-decide slice:
// RBAC-based authorization decisions. Implements runtime/auth.Authorizer.
package authorizationdecide

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time check: Service implements auth.Authorizer.
var _ auth.Authorizer = (*Service)(nil)

// Service implements RBAC authorization decisions.
type Service struct {
	roleRepo ports.RoleRepository
	logger   *slog.Logger
}

// NewService creates an authorization-decide Service.
func NewService(roleRepo ports.RoleRepository, logger *slog.Logger) *Service {
	return &Service{roleRepo: roleRepo, logger: logger}
}

// Authorize checks whether the subject has a role granting the action on the
// resource.
func (s *Service) Authorize(ctx context.Context, subject, resource, action string) (bool, error) {
	roles, err := s.roleRepo.GetByUserID(ctx, subject)
	if err != nil {
		return false, fmt.Errorf("authorization-decide: get roles: %w", err)
	}

	for _, role := range roles {
		if role.HasPermission(resource, action) {
			s.logger.Debug("authorization granted",
				slog.String("subject", subject),
				slog.String("resource", resource),
				slog.String("action", action),
				slog.String("role", role.Name),
			)
			return true, nil
		}
	}

	return false, nil
}
