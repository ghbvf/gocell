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

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time assertion: PGRoleRepo implements ports.RoleRepository.
var _ ports.RoleRepository = (*PGRoleRepo)(nil)

// PGRoleRepo is the cell-private PostgreSQL implementation of ports.RoleRepository.
// It reads/writes the `roles` and `role_assignments` tables (migration 019).
type PGRoleRepo struct {
	db       pgExecutor
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
		db:       newPGExecutor(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

const (
	upsertRoleSQL = `
INSERT INTO roles (id, name, permissions, created_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (id) DO UPDATE
  SET name        = EXCLUDED.name,
      permissions = EXCLUDED.permissions`

	selectRoleByIDSQL = `
SELECT id, name, permissions, created_at
FROM roles
WHERE id = $1`

	selectRolesByUserIDSQL = `
SELECT r.id, r.name, r.permissions, r.created_at
FROM roles r
JOIN role_assignments ra ON ra.role_id = r.id
WHERE ra.user_id = $1`

	insertAssignmentSQL = `
INSERT INTO role_assignments (user_id, role_id, granted_at)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, role_id) DO NOTHING`

	deleteAssignmentSQL = `
DELETE FROM role_assignments
WHERE user_id = $1 AND role_id = $2`

	// removeIfNotLastSQL atomically removes the admin role assignment from $1
	// only if at least one other *effective* admin remains. Effective admin =
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
	// The migration-024 effective_admin_invariant_on_role_assignments trigger
	// remains the safety net for any direct DELETE that bypasses this CTE path.
	removeIfNotLastSQL = `
WITH lock_acquired AS (
    SELECT pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', 0))
),
others AS (
    SELECT u.id FROM users u
    JOIN role_assignments ra ON ra.user_id = u.id
    WHERE ra.role_id = 'admin' AND u.status = 'active' AND u.id <> $1
    FOR UPDATE OF u
),
deleted AS (
    DELETE FROM role_assignments
    WHERE user_id = $1 AND role_id = $2
      AND EXISTS (SELECT 1 FROM others)
    RETURNING user_id
)
SELECT
    EXISTS(SELECT 1 FROM role_assignments WHERE user_id = $1 AND role_id = $2) AS user_held_role,
    EXISTS(SELECT 1 FROM deleted)                                              AS was_deleted`

	countByRoleSQL = `
SELECT COUNT(*)::INT FROM role_assignments WHERE role_id = $1`

	// countEffectiveAdminsSQL counts users that are simultaneously
	// status='active' AND hold the admin role. This is the canonical
	// last-admin invariant counter consumed via the EffectiveAdminCounter
	// sealed interface (S4.0). Advisory lock is taken inside the CTE so the
	// read serializes with concurrent mutation guards (CTE prelude in
	// removeIfNotLastSQL + migration-024 triggers all share the same key).
	//
	// IMPORTANT: This query MUST only be executed within an open write
	// transaction. The advisory lock (pg_advisory_xact_lock) is transaction-
	// scoped: it is automatically released when the enclosing transaction
	// commits or rolls back. Calling this outside a transaction defeats the
	// serialization guarantee — the lock acquires and immediately releases,
	// leaving a window for concurrent mutations. If a lock-free diagnostic
	// variant is needed (e.g. for observability reads), add a separate query
	// without the advisory-lock CTE rather than lifting the constraint here.
	countEffectiveAdminsSQL = `
WITH lock_acquired AS (
    SELECT pg_advisory_xact_lock(hashtextextended('gocell.accesscore.last_admin', 0))
)
SELECT COUNT(*)::INT
FROM role_assignments ra
JOIN users u ON u.id = ra.user_id
WHERE ra.role_id = 'admin' AND u.status = 'active'`
)

// Create upserts a role (seed/bootstrap semantics: existing role is overwritten).
func (r *PGRoleRepo) Create(ctx context.Context, role *domain.Role) error {
	permJSON, err := json.Marshal(role.Permissions)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: marshal permissions", err)
	}
	_, err = r.db.Exec(ctx, upsertRoleSQL,
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

// GetByID fetches a role by primary key. Returns ErrAuthRoleNotFound when absent.
func (r *PGRoleRepo) GetByID(ctx context.Context, id string) (*domain.Role, error) {
	row := r.db.QueryRow(ctx, selectRoleByIDSQL, id)
	role, err := scanRole(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("id=%s", id)))
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: get-by-id", err)
	}
	return role, nil
}

// GetByUserID returns all roles assigned to the user. Returns an empty slice
// when the user has no roles (mirrors mem behavior).
func (r *PGRoleRepo) GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error) {
	rows, err := r.db.Query(ctx, selectRolesByUserIDSQL, userID)
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

// AssignToUser assigns a role to a user. Idempotent: returns changed=false when
// the assignment already existed. Returns ErrAuthRoleNotFound when the role does
// not exist (FK on role_id). Returns ErrAuthUserNotFound when the user does not
// exist (FK on user_id). Fallback for unknown FK violations returns ErrAuthRoleNotFound.
func (r *PGRoleRepo) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	tag, err := r.db.Exec(ctx, insertAssignmentSQL,
		userID,
		roleID,
		r.clock.Now(),
	)
	if err != nil {
		if isForeignKeyViolation(err) {
			switch fkConstraintName(err) {
			case "role_assignments_user_id_fkey":
				return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
					errcode.WithCategory(errcode.CategoryDomain),
					errcode.WithInternal(fmt.Sprintf("user_id=%s", userID)))
			default:
				// role_assignments_role_id_fkey or unknown — treat as role not found.
				return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
					errcode.WithCategory(errcode.CategoryDomain),
					errcode.WithInternal(fmt.Sprintf("role_id=%s", roleID)))
			}
		}
		return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: assign-to-user", err)
	}
	// ON CONFLICT DO NOTHING: RowsAffected==1 means inserted, 0 means already existed.
	return tag.RowsAffected() == 1, nil
}

// RemoveFromUser removes a role assignment. Idempotent — no error when the
// assignment did not exist.
func (r *PGRoleRepo) RemoveFromUser(ctx context.Context, userID, roleID string) error {
	_, err := r.db.Exec(ctx, deleteAssignmentSQL, userID, roleID)
	if err != nil {
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last admin",
				errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q", roleID, userID)))
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
// Returns:
//   - (true, nil)  — role was held and successfully removed.
//   - (false, nil) — role was not held (idempotent no-op).
//   - (false, ErrAuthLastAdminProtected) — admin path only; removal would
//     leave zero effective admins. Both the app-level CTE detect path and
//     the DB trigger safety-net path return the same errcode so client
//     handlers match a single business invariant.
func (r *PGRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	if roleID != auth.RoleAdmin {
		// Non-admin role: plain DELETE, no last-holder check. Trigger
		// (migration 024) also short-circuits on `role_id <> 'admin'`.
		tag, err := r.db.Exec(ctx, deleteAssignmentSQL, userID, roleID)
		if err != nil {
			return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"role_repo: remove-if-not-last (non-admin)", err)
		}
		return tag.RowsAffected() == 1, nil
	}

	var userHeldRole, wasDeleted bool
	row := r.db.QueryRow(ctx, removeIfNotLastSQL, userID, roleID)
	if err := row.Scan(&userHeldRole, &wasDeleted); err != nil {
		if isLastAdminProtected(err) {
			// DB trigger fired — safety net for any direct DELETE bypass of CTE.
			return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last admin",
				errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q", roleID, userID)))
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
			errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q", roleID, userID)))
	}
}

// CountByRole returns the total count of role_assignments for roleID
// regardless of user status. Used for bootstrap idempotency
// (adminprovision); MUST NOT be used as the last-admin invariant counter —
// see CountEffectiveAdmins.
func (r *PGRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	var count int
	row := r.db.QueryRow(ctx, countByRoleSQL, roleID)
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
// CONTRACT: Must be called within an open write transaction. The advisory lock
// is transaction-scoped (pg_advisory_xact_lock) and releases on commit/rollback;
// outside-transaction callers acquire and immediately release the lock, defeating
// the invariant guarantee. The current sole caller (domain.LastAdminGuard.CheckRemove
// via identitymanage/rbacassign service entry points) always runs inside
// txRunner.RunInTx — this contract is satisfied. If a lock-free read is ever
// needed for diagnostics or observability, add a dedicated variant without the
// advisory-lock CTE rather than relaxing this contract.
func (r *PGRoleRepo) CountEffectiveAdmins(ctx context.Context) (int, error) {
	var count int
	row := r.db.QueryRow(ctx, countEffectiveAdminsSQL)
	if err := row.Scan(&count); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: count-effective-admins", err)
	}
	return count, nil
}

// ListByUserID returns a paginated, sorted list of roles assigned to userID.
// Mirrors the mem implementation: loads all roles for the user, then applies
// query.Sort and query.ApplyCursor in Go.
func (r *PGRoleRepo) ListByUserID(ctx context.Context, userID string, params query.ListParams) ([]*domain.Role, error) {
	roles, err := r.GetByUserID(ctx, userID)
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
