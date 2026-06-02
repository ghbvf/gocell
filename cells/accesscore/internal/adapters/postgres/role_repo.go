package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time assertion: PGRoleRepo implements ports.RoleRepository.
var _ ports.RoleRepository = (*PGRoleRepo)(nil)

// PGRoleRepo is the cell-private PostgreSQL implementation of ports.RoleRepository.
// It reads/writes the `roles` and `role_assignments` tables (migration 019).
type PGRoleRepo struct {
	db       pgexec.PGExecutor
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewPGRoleRepo constructs a PGRoleRepo. Fails fast on nil dependencies.
func NewPGRoleRepo(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	clk clock.Clock,
) (*PGRoleRepo, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGRoleRepo: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGRoleRepo: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGRoleRepo: clock must not be nil")
	}
	return &PGRoleRepo{
		db:       pgexec.New(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

// fmtRoleIDUserID is the internal-message format string for role/user pair
// diagnostics. Used in RemoveFromUser and RemoveFromUserIfNotLast (3 sites).
const fmtRoleIDUserID = "role_id=%q user_id=%q"

const (
	upsertRoleSQL = `
INSERT INTO roles (tenant_id, id, name, permissions, created_at)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tenant_id, id) DO UPDATE
  SET name        = EXCLUDED.name,
      permissions = EXCLUDED.permissions`

	selectRoleByIDSQL = `
SELECT id, name, permissions, created_at
FROM roles
WHERE tenant_id = $1 AND id = $2`

	selectRolesByUserIDSQL = `
SELECT r.id, r.name, r.permissions, r.created_at
FROM roles r
JOIN role_assignments ra ON ra.role_id = r.id AND ra.tenant_id = r.tenant_id
WHERE ra.tenant_id = $1 AND ra.user_id = $2`

	insertAssignmentSQL = `
INSERT INTO role_assignments (tenant_id, user_id, role_id, granted_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (tenant_id, user_id, role_id) DO NOTHING`

	deleteAssignmentSQL = `
DELETE FROM role_assignments
WHERE tenant_id = $1 AND user_id = $2 AND role_id = $3`

	// removeIfNotLastSQL atomically removes the admin role assignment from $1
	// only if either (a) the target user is not currently active (locked /
	// suspended targets can't reduce the effective-admin set, matching the
	// migration-024 trigger which RETURNs OLD when target is non-active), or
	// (b) at least one OTHER *effective* admin remains. Effective admin =
	// (users.status='active' AND has admin role) — a locked/suspended admin
	// peer does NOT keep the system administrable (S4.0 invariant upgrade).
	//
	// The CTE acquires (a) a transaction-scoped advisory lock on
	// 'gocell.accesscore.last_admin' so any concurrent guard path (this CTE
	// plus the migration-024 trigger on `users`) serializes, and (b)
	// FOR UPDATE OF u on the *other* active-admin user rows so concurrent
	// admin-revoke / lock / delete transactions block until the holder set is
	// stable. READ COMMITTED isolation is sufficient under these locks.
	//
	// MATERIALIZED on lock_acquired forces evaluation: PG 12+ inlines
	// unreferenced CTEs and a planner that drops the lock CTE would defeat
	// serialization (the volatile pg_advisory_xact_lock would never run).
	// We additionally CROSS JOIN lock_acquired into the deleted CTE so even a
	// future planner that ignores MATERIALIZED keeps the dependency.
	//
	// target_status reads the current target user status (NULL if user does
	// not exist); used to short-circuit the last-admin check when target is
	// non-active.
	//
	// The migration-024 effective_admin_invariant_on_role_assignments trigger
	// remains the safety net for any direct DELETE that bypasses this CTE path.
	// removeIfNotLastSQL is a per-tenant CTE. $1=tenant_id, $2=user_id, $3=role_id.
	// The advisory lock key includes hashtext(tenant_id) so concurrent guards
	// in different tenants do NOT serialize against each other (migration 046
	// per-tenant effective-admin invariant).
	removeIfNotLastSQL = `
WITH lock_acquired AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', hashtext($1))) AS locked
),
target_status AS (
    SELECT status FROM users WHERE tenant_id = $1 AND id = $2
),
others AS (
    SELECT u.id FROM users u
    JOIN role_assignments ra ON ra.user_id = u.id AND ra.tenant_id = u.tenant_id
    WHERE ra.tenant_id = $1 AND ra.role_id = 'admin' AND u.status = 'active' AND u.id <> $2
    FOR UPDATE OF u
),
deleted AS (
    DELETE FROM role_assignments
    WHERE tenant_id = $1 AND user_id = $2 AND role_id = $3
      AND EXISTS (SELECT 1 FROM lock_acquired)
      AND (
          (SELECT status FROM target_status) IS DISTINCT FROM 'active'
          OR EXISTS (SELECT 1 FROM others)
      )
    RETURNING user_id
)
SELECT
    EXISTS(SELECT 1 FROM role_assignments WHERE tenant_id = $1 AND user_id = $2 AND role_id = $3) AS user_held_role,
    EXISTS(SELECT 1 FROM deleted)                                                                   AS was_deleted`

	countByRoleSQL = `
SELECT COUNT(*)::INT FROM role_assignments WHERE tenant_id = $1 AND role_id = $2`

	// countEffectiveAdminsSQL is per-tenant. $1=tenant_id. Advisory lock key
	// includes hashtext(tenant_id) so per-tenant serialization is independent.
	countEffectiveAdminsSQL = `
WITH lock_acquired AS MATERIALIZED (
    SELECT pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', hashtext($1))) AS locked
)
SELECT COUNT(*)::INT
FROM role_assignments ra
JOIN users u ON u.id = ra.user_id AND u.tenant_id = ra.tenant_id
CROSS JOIN lock_acquired
WHERE ra.tenant_id = $1 AND ra.role_id = 'admin' AND u.status = 'active'`
)

// Create upserts a role (seed/bootstrap semantics: existing role is overwritten).
// The SQL uses ON CONFLICT (tenant_id, id) DO UPDATE — the composite PK on `roles`.
// A unique-violation (SQLSTATE 23505) is therefore structurally impossible
// here; any error is a genuine infra failure, classified as ErrInternal.
func (r *PGRoleRepo) Create(ctx context.Context, t tenant.TenantID, role *domain.Role) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	permJSON, err := json.Marshal(role.Permissions)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: marshal permissions", err)
	}
	_, err = r.db.Exec(
		ctx, upsertRoleSQL,
		string(t),
		role.ID,
		role.Name,
		permJSON,
		r.clock.Now(),
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: create", err)
	}
	return nil
}

// GetByID fetches a role by composite (tenant_id, id) key. Returns ErrAuthRoleNotFound when absent.
func (r *PGRoleRepo) GetByID(ctx context.Context, t tenant.TenantID, id string) (*domain.Role, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	row := r.db.QueryRow(ctx, selectRoleByIDSQL, string(t), id)
	role, err := scanRole(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", id))))
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: get-by-id", err)
	}
	return role, nil
}

// GetByUserID returns all roles assigned to the user in the given tenant.
// Returns an empty slice when the user has no roles (mirrors mem behavior).
func (r *PGRoleRepo) GetByUserID(ctx context.Context, t tenant.TenantID, userID string) ([]*domain.Role, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	rows, err := r.db.Query(ctx, selectRolesByUserIDSQL, string(t), userID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: get-by-user-id", err)
	}
	defer rows.Close()

	result := make([]*domain.Role, 0)
	for rows.Next() {
		role, scanErr := scanRoleFromRows(rows)
		if scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: scan role", scanErr)
		}
		result = append(result, role)
	}
	if rows.Err() != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: rows err", rows.Err())
	}
	return result, nil
}

// fkConstraintName extracts the ConstraintName from a PG FK violation error.
func fkConstraintName(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// AssignToUser assigns a role to a user within a tenant. Idempotent: returns changed=false when
// the assignment already existed. Returns ErrAuthRoleNotFound when the role does
// not exist (FK on role_id). Returns ErrAuthUserNotFound when the user does not
// exist (FK on user_id). Fallback for unknown FK violations returns ErrAuthRoleNotFound.
func (r *PGRoleRepo) AssignToUser(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	tag, err := r.db.Exec(
		ctx, insertAssignmentSQL,
		string(t),
		userID,
		roleID,
		r.clock.Now(),
	)
	if err != nil {
		if pgquery.IsForeignKeyViolation(err) {
			switch fkConstraintName(err) {
			case "role_assignments_user_id_fkey":
				return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
					errcode.WithCategory(errcode.CategoryDomain),
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("user_id=%s", userID))))
			default:
				// role_assignments_role_id_fkey or unknown — treat as role not found.
				return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
					errcode.WithCategory(errcode.CategoryDomain),
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("role_id=%s", roleID))))
			}
		}
		return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: assign-to-user", err)
	}
	// ON CONFLICT DO NOTHING: RowsAffected==1 means inserted, 0 means already existed.
	return tag.RowsAffected() == 1, nil
}

// RemoveFromUser removes a role assignment. Idempotent — no error when the
// assignment did not exist.
//
// SAFETY: callers needing the at-least-one-effective-admin invariant
// (revoke admin role, demote sole admin) must use RemoveFromUserIfNotLast,
// which combines the application-layer CTE check with the migration-024
// trigger safety net. RemoveFromUser issues a plain DELETE and relies
// SOLELY on the trigger to enforce the invariant — when the trigger
// blocks the DELETE, isLastAdminProtected translates SQLSTATE P0001 into
// ErrAuthLastAdminProtected (HTTP 403). The single legitimate caller
// passing roleID == auth.RoleAdmin is adminprovision.Compensate, which
// runs after a setup failure: if the just-provisioned admin is the only
// effective admin, the trigger correctly blocks the cleanup and leaves
// the operator with a usable account rather than an unusable system.
func (r *PGRoleRepo) RemoveFromUser(ctx context.Context, t tenant.TenantID, userID, roleID string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	_, err := r.db.Exec(ctx, deleteAssignmentSQL, string(t), userID, roleID)
	if err != nil {
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last admin",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(fmtRoleIDUserID, roleID, userID))))
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: remove-from-user", err)
	}
	return nil
}

// RemoveFromUserIfNotLast removes a role assignment with admin-scoped
// last-effective-admin protection (ADR-admin-invariant §3.2, S4.0). For
// roleID == auth.RoleAdmin the CTE acquires an advisory xact lock plus
// FOR UPDATE OF u on the other active-admin users, atomically serializing
// concurrent revocations / locks / deletes. The DELETE only fires when at
// least one OTHER effective admin (status='active' AND admin) remains. For
// any other roleID the operation is a plain idempotent DELETE (matches the
// migration-024 trigger scope: `IF OLD.role_id <> 'admin' THEN RETURN OLD;`).
//
// CONTRACT (runtime-enforced for admin path): the admin-role branch must
// be called within an open write transaction so the CTE's
// pg_advisory_xact_lock scopes to the caller's tx, not a one-shot pool
// connection. The function fail-fasts with ErrInternal when roleID equals
// auth.RoleAdmin and no pgx.Tx is present under
// kernel/persistence.TxCtxKey. The non-admin path stays pool-driven (no
// lock involved); calling it outside a tx is fine.
//
// Returns:
//   - (true, nil)  — role was held and successfully removed.
//   - (false, nil) — role was not held (idempotent no-op).
//   - (false, ErrAuthLastAdminProtected) — admin path only; removal would
//     leave zero effective admins. Both the app-level CTE detect path and
//     the DB trigger safety-net path return the same errcode so client
//     handlers match a single business invariant.
func (r *PGRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, t tenant.TenantID, userID, roleID string) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	if roleID != auth.RoleAdmin {
		// Non-admin role: plain DELETE, no last-holder check. Trigger
		// (migration 046) also short-circuits on `role_id <> 'admin'`.
		tag, err := r.db.Exec(ctx, deleteAssignmentSQL, string(t), userID, roleID)
		if err != nil {
			return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"role_repo: remove-if-not-last (non-admin)", err)
		}
		return tag.RowsAffected() == 1, nil
	}

	tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx)
	if !ok || tx == nil {
		return false, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"role_repo: remove-if-not-last (admin) must be called inside a transaction (no pgx.Tx in ctx)")
	}

	var userHeldRole, wasDeleted bool
	// removeIfNotLastSQL: $1=tenant_id, $2=user_id, $3=role_id
	row := tx.QueryRow(ctx, removeIfNotLastSQL, string(t), userID, roleID)
	if err := row.Scan(&userHeldRole, &wasDeleted); err != nil {
		if isLastAdminProtected(err) {
			// DB trigger fired — safety net for any direct DELETE bypass of CTE.
			return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last admin",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(fmtRoleIDUserID, roleID, userID))))
		}
		return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"role_repo: remove-if-not-last", err)
	}

	switch {
	case !userHeldRole:
		// User did not hold the role — idempotent no-op.
		return false, nil
	case wasDeleted:
		// Role held and removed successfully.
		return true, nil
	default:
		// Admin held but not removed: removing it would leave zero effective
		// admins (no peer with status='active' AND admin role). Same errcode
		// as the DB trigger path — single business invariant.
		return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
			"cannot revoke admin: removing this assignment would leave the system with no effective admin; assign admin to an active user first",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(fmtRoleIDUserID, roleID, userID))))
	}
}

// CountByRole returns the total count of role_assignments for roleID
// regardless of user status. Used for bootstrap idempotency
// (adminprovision); MUST NOT be used as the last-admin invariant counter —
// see CountEffectiveAdmins.
func (r *PGRoleRepo) CountByRole(ctx context.Context, t tenant.TenantID, roleID string) (int, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	var count int
	row := r.db.QueryRow(ctx, countByRoleSQL, string(t), roleID)
	if err := row.Scan(&count); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: count-by-role", err)
	}
	return count, nil
}

// CountEffectiveAdmins returns the number of users that are simultaneously
// status='active' AND hold the admin role. Satisfies the domain.
// EffectiveAdminCounter sealed interface (S4.0 invariant counter).
//
// Acquires advisory xact lock 'gocell.accesscore.last_admin' inside the CTE
// so concurrent guard paths (this read, removeIfNotLastSQL CTE, and the
// migration-024 trigger on users) serialize.
//
// CONTRACT (runtime-enforced): Must be called within an open write
// transaction. The advisory lock is transaction-scoped
// (pg_advisory_xact_lock) and releases on commit/rollback; outside-transaction
// callers would acquire and immediately release the lock, defeating the
// invariant guarantee. The function fail-fasts with ErrInternal when no
// pgx.Tx is present under kernel/persistence.TxCtxKey — same shape as
// PGSetupLock.Acquire / OutboxWriter.Write. If a lock-free read is ever
// needed for diagnostics or observability, add a dedicated variant without
// the advisory-lock CTE rather than relaxing this contract.
func (r *PGRoleRepo) CountEffectiveAdmins(ctx context.Context, t tenant.TenantID) (int, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx)
	if !ok || tx == nil {
		return 0, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"role_repo: count-effective-admins must be called inside a transaction (no pgx.Tx in ctx)")
	}
	var count int
	// countEffectiveAdminsSQL: $1=tenant_id
	row := tx.QueryRow(ctx, countEffectiveAdminsSQL, string(t))
	if err := row.Scan(&count); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: count-effective-admins", err)
	}
	return count, nil
}

// effectiveAdminExistsSQL is the lock-free read counterpart to
// countEffectiveAdminsSQL. No advisory lock and no tx requirement —
// designed for fast-path checks (setup retirement, provisioner.Status)
// where eventual consistency is acceptable and the result does not feed
// into the at-least-one invariant decision.
// effectiveAdminExistsSQL is per-tenant. $1=tenant_id.
const effectiveAdminExistsSQL = `
SELECT EXISTS (
    SELECT 1 FROM role_assignments ra
    JOIN users u ON u.id = ra.user_id AND u.tenant_id = ra.tenant_id
    WHERE ra.tenant_id = $1 AND ra.role_id = 'admin' AND u.status = 'active'
)`

// EffectiveAdminExists implements ports.RoleRepository — see the port godoc
// for fast-path semantics. Pool-driven (no tx required, no advisory lock).
func (r *PGRoleRepo) EffectiveAdminExists(ctx context.Context, t tenant.TenantID) (bool, error) {
	if err := t.Validate(); err != nil {
		return false, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "role_repo: invalid tenant", err)
	}
	var exists bool
	row := r.db.QueryRow(ctx, effectiveAdminExistsSQL, string(t))
	if err := row.Scan(&exists); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: effective-admin-exists", err)
	}
	return exists, nil
}

// ListByUserID returns a paginated, sorted list of roles assigned to userID.
// Mirrors the mem implementation: loads all roles for the user, then applies
// query.Sort and query.ApplyCursor in Go.
func (r *PGRoleRepo) ListByUserID(ctx context.Context, t tenant.TenantID, userID string, params query.ListParams) ([]*domain.Role, error) {
	roles, err := r.GetByUserID(ctx, t, userID)
	if err != nil {
		return nil, fmt.Errorf("role_repo: list-by-user: %w", err)
	}

	query.Sort(roles, params.Sort, compareRoleField)
	result, err := query.ApplyCursor(roles, params, roleFieldValue)
	if err != nil {
		return nil, fmt.Errorf("role_repo: list-by-user: %w", err)
	}
	return result, nil
}

// scanRole scans a pgx.Row into a domain.Role.
func scanRole(row pgx.Row) (*domain.Role, error) {
	var role domain.Role
	var permJSON []byte
	var createdAt interface{}
	err := row.Scan(&role.ID, &role.Name, &permJSON, &createdAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(permJSON, &role.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	if role.Permissions == nil {
		role.Permissions = []domain.Permission{}
	}
	return &role, nil
}

// scanRoleFromRows scans a pgx.Rows cursor into a domain.Role.
func scanRoleFromRows(rows pgx.Rows) (*domain.Role, error) {
	var role domain.Role
	var permJSON []byte
	var createdAt interface{}
	if err := rows.Scan(&role.ID, &role.Name, &permJSON, &createdAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(permJSON, &role.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	if role.Permissions == nil {
		role.Permissions = []domain.Permission{}
	}
	return &role, nil
}

func compareRoleField(a, b *domain.Role, field string) int {
	switch field {
	case "name":
		return cmp.Compare(a.Name, b.Name)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

func roleFieldValue(r *domain.Role, field string) any {
	switch field {
	case "name":
		return r.Name
	case "id":
		return r.ID
	default:
		return ""
	}
}
