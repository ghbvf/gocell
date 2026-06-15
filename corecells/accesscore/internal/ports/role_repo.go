package ports

import (
	"context"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// RoleRepository persists and retrieves Role entities and user-role assignments.
//
// Tenancy (#1337 PR-2): roles are per-tenant — every method takes a mandatory
// tenant.TenantID positional parameter immediately after ctx ("in tenant T,
// do X"). There is no by-PK tenant-deriving carve-out here (unlike
// UserRepository.GetByID): a role id is unique only within a tenant, so even a
// by-id read must be tenant-scoped. Implementations apply `AND tenant_id = $N`
// (PG) / tenant filtering (mem) and reject an invalid tenant via tenant.Validate.
type RoleRepository interface {
	GetByID(ctx context.Context, t tenant.TenantID, id string) (*domain.Role, error)
	// GetByUserID returns the roles assigned to userID within tenant t.
	//
	// Row-visibility obligation (#1709, ROWSCOPE-REPO-PARAM-FUNNEL-01): vis is the
	// principal-derived owner-dimension obligation (owner column = role_assignments.user_id).
	// The subject-self read endpoints GET /api/v1/access/roles/{userID} (list + check)
	// pass the principal's RowVisibility so a non-admin who passes the coarse route
	// gate still only reads their own roles — a non-self userID IDOR-collapses to an
	// empty result. SYSTEM callers (rbac enforcement, admin provisioning) pass
	// tenant.SystemRowVisibility(). vis is validated fail-closed; RowScopeAll
	// fail-closes with RowScopeAllUnsupportedError.
	GetByUserID(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, userID string) ([]*domain.Role, error)
	Create(ctx context.Context, t tenant.TenantID, role *domain.Role) error
	// AssignToUser assigns the role to the user. Idempotent.
	// Returns changed=true when the user did not previously hold the role
	// (a real state transition occurred); changed=false when the user already
	// held it (no-op). Callers gate outbox emission on changed so that no-ops
	// do not publish false role-change facts.
	AssignToUser(ctx context.Context, t tenant.TenantID, userID, roleID string) (changed bool, err error)
	RemoveFromUser(ctx context.Context, t tenant.TenantID, userID, roleID string) error
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
	RemoveFromUserIfNotLast(ctx context.Context, t tenant.TenantID, userID, roleID string) (changed bool, err error)
	// CountByRole counts all role_assignments rows for roleID, regardless of
	// user status. Used by adminprovision bootstrap idempotency where a
	// locked/suspended admin still counts as "an admin exists" (so we don't
	// re-create one). DO NOT use for the last-admin invariant — that is
	// CountEffectiveAdmins (status filter required).
	CountByRole(ctx context.Context, t tenant.TenantID, roleID string) (int, error)
	// CountEffectiveAdmins returns the number of users who are simultaneously
	// status='active' AND hold the admin role. This is the canonical invariant
	// counter consumed by domain.LastAdminGuard via the EffectiveAdminCounter
	// sealed interface. Implementations must filter by user status (NOT just
	// count role_assignments) so a locked/suspended admin is excluded.
	CountEffectiveAdmins(ctx context.Context, t tenant.TenantID) (int, error)
	// EffectiveAdminExists is the lock-free read counterpart to
	// CountEffectiveAdmins. Used by setup-retirement and provisioner.Status
	// fast-path checks where eventual consistency is acceptable and acquiring
	// the advisory lock is overkill (these reads happen outside any tx and
	// are not part of the at-least-one invariant decision). Returns true when
	// at least one user satisfies status='active' AND holds the admin role.
	EffectiveAdminExists(ctx context.Context, t tenant.TenantID) (bool, error)
	// ListByUserID returns a paginated list of roles assigned to userID,
	// sorted and filtered per params.
	//
	// Row-visibility obligation (#1709, ROWSCOPE-REPO-PARAM-FUNNEL-01): same
	// owner-dimension contract as GetByUserID (owner column = role_assignments.user_id);
	// a non-self userID under RowScopeSelf/Device collapses to an empty page.
	ListByUserID(
		ctx context.Context,
		t tenant.TenantID,
		vis tenant.RowVisibility,
		userID string,
		params query.ListParams,
	) ([]*domain.Role, error)
}
