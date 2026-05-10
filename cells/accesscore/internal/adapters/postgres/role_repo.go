package postgres

import (
	"cmp"
	"context"
	"encoding/json"
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
)

// Compile-time assertion: PGRoleRepo implements ports.RoleRepository.
var _ ports.RoleRepository = (*PGRoleRepo)(nil)

// PGRoleRepo is the cell-private PostgreSQL implementation of ports.RoleRepository.
// It reads/writes the `roles` and `role_assignments` tables (migration 019).
type PGRoleRepo struct {
	pool     *pgxpool.Pool
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
		pool:     pool,
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

// execCtx executes SQL using the ambient tx from ctx when present.
func (r *PGRoleRepo) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return r.pool.Exec(ctx, sql, args...)
}

// queryRowCtx queries a single row using the ambient tx when present.
func (r *PGRoleRepo) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

// queryCtx queries multiple rows using the ambient tx when present.
func (r *PGRoleRepo) queryCtx(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return r.pool.Query(ctx, sql, args...)
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

	// removeIfNotLastSQL atomically checks and removes the assignment only if
	// another holder remains. The CTE returns two booleans to distinguish
	// between the three outcome branches without multiple round-trips.
	removeIfNotLastSQL = `
WITH holders AS (
    SELECT user_id FROM role_assignments WHERE role_id = $2
),
deleted AS (
    DELETE FROM role_assignments
    WHERE user_id = $1 AND role_id = $2
      AND (SELECT COUNT(*) FROM holders) > 1
    RETURNING user_id
)
SELECT
    (SELECT COUNT(*) FROM holders WHERE user_id = $1) > 0 AS user_held_role,
    (SELECT COUNT(*) FROM deleted) > 0 AS did_delete`

	countByRoleSQL = `
SELECT COUNT(*)::INT FROM role_assignments WHERE role_id = $1`
)

// Create upserts a role (seed/bootstrap semantics: existing role is overwritten).
func (r *PGRoleRepo) Create(ctx context.Context, role *domain.Role) error {
	permJSON, err := json.Marshal(role.Permissions)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: marshal permissions", err)
	}
	_, err = r.execCtx(ctx, upsertRoleSQL,
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
	row := r.queryRowCtx(ctx, selectRoleByIDSQL, id)
	role, err := scanRole(row)
	if err != nil {
		if err == pgx.ErrNoRows {
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
	rows, err := r.queryCtx(ctx, selectRolesByUserIDSQL, userID)
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

// AssignToUser assigns a role to a user. Idempotent: returns changed=false when
// the assignment already existed. Returns ErrAuthRoleNotFound when the role does
// not exist (FK violation).
func (r *PGRoleRepo) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	tag, err := r.execCtx(ctx, insertAssignmentSQL,
		userID,
		roleID,
		r.clock.Now(),
	)
	if err != nil {
		if isForeignKeyViolation(err) {
			return false, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound, "role not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("role_id=%s", roleID)))
		}
		return false, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: assign-to-user", err)
	}
	// ON CONFLICT DO NOTHING: RowsAffected==1 means inserted, 0 means already existed.
	return tag.RowsAffected() == 1, nil
}

// RemoveFromUser removes a role assignment. Idempotent — no error when the
// assignment did not exist.
func (r *PGRoleRepo) RemoveFromUser(ctx context.Context, userID, roleID string) error {
	_, err := r.execCtx(ctx, deleteAssignmentSQL, userID, roleID)
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

// RemoveFromUserIfNotLast atomically removes the role assignment only if at
// least one other holder will remain after the removal. Returns:
//   - (true, nil)  — role was held and successfully removed.
//   - (false, nil) — role was not held (idempotent no-op).
//   - (false, ErrAuthForbidden) — user is the sole holder; removal refused.
func (r *PGRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	var userHeldRole, didDelete bool
	row := r.queryRowCtx(ctx, removeIfNotLastSQL, userID, roleID)
	if err := row.Scan(&userHeldRole, &didDelete); err != nil {
		if isLastAdminProtected(err) {
			// DB trigger fired — should not happen with the CTE but handle as safety net.
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
	case didDelete:
		// Role held and removed successfully.
		return true, nil
	default:
		// Role held but not removed: user is the sole holder.
		return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"cannot revoke role: this is the only holder; assign the role to another user first",
			errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q", roleID, userID)))
	}
}

// CountByRole returns the number of users assigned to the given role.
func (r *PGRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	var count int
	row := r.queryRowCtx(ctx, countByRoleSQL, roleID)
	if err := row.Scan(&count); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "role_repo: count-by-role", err)
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
