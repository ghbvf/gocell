package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
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
	// RemoveFromUserIfNotLast removes a role assignment with admin-scoped
	// last-effective-admin protection (ADR-admin-invariant §3.2, S4.0):
	//   - When roleID == auth.RoleAdmin, atomically check that another
	//     *effective* admin (user.status='active' AND has admin role) remains
	//     and refuse with ErrAuthLastAdminProtected (KindPermissionDenied / 403)
	//     when removing this assignment would leave zero effective admins.
	//     Backends with a DB-level safety trigger return the same errcode for
	//     direct-DELETE bypass paths.
	//   - For any other roleID, behave as a plain idempotent delete (no
	//     last-holder check) — non-admin roles MUST be revocable down to zero
	//     holders. This matches the DB trigger scope (migration 024:
	//     `IF OLD.role_id <> 'admin' THEN RETURN OLD;`).
	// Implementations must guarantee atomicity for the admin path (no TOCTOU
	// gap between count check and removal). Returns changed=true when the user
	// actually held the role and it was removed; changed=false when the user
	// did not hold the role (no-op) OR when removal was refused. Callers gate
	// outbox emission on changed.
	RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (changed bool, err error)
	// CountByRole counts all role_assignments rows for roleID, regardless of
	// user status. Used by adminprovision bootstrap idempotency where a
	// locked/suspended admin still counts as "an admin exists" (so we don't
	// re-create one). DO NOT use for the last-admin invariant — that is
	// CountEffectiveAdmins (status filter required).
	CountByRole(ctx context.Context, roleID string) (int, error)
	// CountEffectiveAdmins returns the number of users who are simultaneously
	// status='active' AND hold the admin role. This is the canonical invariant
	// counter consumed by domain.LastAdminGuard via the EffectiveAdminCounter
	// sealed interface. Implementations must filter by user status (NOT just
	// count role_assignments) so a locked/suspended admin is excluded.
	CountEffectiveAdmins(ctx context.Context) (int, error)
	// ListByUserID returns a paginated list of roles assigned to userID,
	// sorted and filtered per params.
	ListByUserID(ctx context.Context, userID string, params query.ListParams) ([]*domain.Role, error)
}
