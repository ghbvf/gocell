package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
)

// RoleRepository persists and retrieves Role entities and user-role assignments.
type RoleRepository interface {
	GetByID(ctx context.Context, id string) (*domain.Role, error)
	GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error)
	Create(ctx context.Context, role *domain.Role) error
	AssignToUser(ctx context.Context, userID, roleID string) error
	RemoveFromUser(ctx context.Context, userID, roleID string) error
	// RemoveFromUserIfNotLast atomically removes the role from the user only
	// if at least one other holder will remain. Returns ErrAuthForbidden if
	// the user is the sole holder. Implementations must guarantee atomicity
	// (no TOCTOU gap between count check and removal).
	RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) error
	CountByRole(ctx context.Context, roleID string) (int, error)
}
