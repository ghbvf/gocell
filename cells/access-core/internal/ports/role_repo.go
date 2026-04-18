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
	// AssignToUser assigns the role to the user. Idempotent.
	// Returns changed=true when the user did not previously hold the role
	// (a real state transition occurred); changed=false when the user already
	// held it (no-op). Callers gate outbox emission on changed so that no-ops
	// do not publish false role-change facts.
	AssignToUser(ctx context.Context, userID, roleID string) (changed bool, err error)
	RemoveFromUser(ctx context.Context, userID, roleID string) error
	// RemoveFromUserIfNotLast atomically removes the role from the user only
	// if at least one other holder will remain. Returns ErrAuthForbidden if
	// the user is the sole holder. Implementations must guarantee atomicity
	// (no TOCTOU gap between count check and removal).
	// Returns changed=true when the user actually held the role and it was
	// removed; changed=false when the user did not hold the role (no-op).
	// Callers gate outbox emission on changed.
	RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (changed bool, err error)
	CountByRole(ctx context.Context, roleID string) (int, error)
}
