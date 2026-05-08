// Package postgres provides a PostgreSQL implementation of accesscore internal ports.
//
// It does NOT import adapters/postgres — it resolves the ambient pgx.Tx from
// ctx via persistence.TxCtxKey (kernel-owned) and falls back to the pool for
// non-transactional reads. This keeps the cell decoupled from the adapter
// layer (CLAUDE.md: cells/ must not import adapters/).
//
// ref: cells/accesscore/internal/adapters/postgres/user_repo.go (Dev A pattern)
// ref: cells/configcore/internal/adapters/postgres/session.go (TxCtxKey pattern)
// ref: jackc/pgx v5 pgconn PgError 23505 unique_violation detection
// ref: jackc/pgx v5 pgconn PgError 23503 foreign_key_violation detection
// ref: PostgreSQL indexes-partial.html (partial index conflict via ConstraintName)
// ref: adapters/postgres/migrations/020_role_assignments_fk.sql (FK + cascade)
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

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

// Compile-time assertion: PGRoleRepository implements ports.RoleRepository.
var _ ports.RoleRepository = (*PGRoleRepository)(nil)

// adminRoleConstraint is the partial index name that enforces at-most-one admin
// role holder. The INSERT ON CONFLICT DO NOTHING clause absorbs the PK duplicate
// scenario; this partial index collision must be caught by the application.
const adminRoleConstraint = "idx_role_assignments_single_admin"

const (
	insertRoleSQL = `
INSERT INTO roles (id, name, permissions, created_at)
VALUES ($1, $2, $3, $4)`

	selectRoleByIDSQL = `
SELECT id, name, permissions, created_at
FROM roles
WHERE id = $1`

	// insertRoleAssignmentSQL assigns a role to a user.
	//
	// ON CONFLICT (user_id, role_id) DO NOTHING absorbs the PK duplicate case
	// (same user already holds the role) → RowsAffected==0, changed=false.
	//
	// NOTE: idx_role_assignments_single_admin partial index conflict is NOT
	// absorbed by DO NOTHING — that clause only matches the explicit conflict
	// target (user_id, role_id). A partial-index violation produces a 23505
	// error with ConstraintName=="idx_role_assignments_single_admin", which the
	// application catches and maps to ErrAuthRoleDuplicate.
	insertRoleAssignmentSQL = `
INSERT INTO role_assignments (user_id, role_id, assigned_at)
VALUES ($1, $2, $3)
ON CONFLICT (user_id, role_id) DO NOTHING`

	deleteRoleAssignmentSQL = `
DELETE FROM role_assignments
WHERE user_id = $1 AND role_id = $2`

	// acquireRoleAdvisoryLockSQL acquires a transaction-scoped advisory lock
	// keyed by role_id. MUST be issued as a separate statement BEFORE
	// removeIfNotLastSQL — embedding pg_advisory_xact_lock in a CTE alongside
	// the count/delete does NOT serialize concurrent callers because Read
	// Committed takes a single statement-start snapshot for the whole
	// statement, so the cnt CTE is computed against the pre-lock snapshot.
	//
	// Issuing the lock as its own statement forces the next statement to
	// take a fresh snapshot taken after this lock is held, so concurrent
	// transactions waiting on the lock observe each other's committed
	// DELETEs in the cnt CTE that follows.
	//
	// ref: https://www.postgresql.org/docs/current/queries-with.html
	//      "The sub-statements in WITH are executed concurrently with each
	//       other and with the main query."
	// ref: https://www.postgresql.org/docs/current/transaction-iso.html
	//      Read Committed: each statement sees a snapshot taken at its start.
	// ref: https://www.postgresql.org/docs/current/functions-admin.html
	//      pg_advisory_xact_lock — released at end of current transaction.
	acquireRoleAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('role:' || $1, 0))`

	// removeIfNotLastSQL is the count+delete CTE that runs AFTER the advisory
	// lock has been acquired in a previous statement. Returns three signals so
	// the Go caller can distinguish:
	//   - held=false             → user did not hold role (idempotent no-op)
	//   - held=true && deleted=1 → revoke succeeded
	//   - held=true && deleted=0 → user is sole holder, ErrAuthForbidden
	removeIfNotLastSQL = `
WITH holds AS (
    SELECT 1 FROM role_assignments WHERE user_id = $1 AND role_id = $2
),
cnt AS (
    SELECT COUNT(*) AS n FROM role_assignments WHERE role_id = $2
),
del AS (
    DELETE FROM role_assignments
    WHERE user_id = $1 AND role_id = $2 AND (SELECT n FROM cnt) > 1
    RETURNING 1
)
SELECT
    EXISTS(SELECT 1 FROM holds) AS held,
    (SELECT n FROM cnt)         AS holders,
    (SELECT count(*) FROM del)  AS deleted`

	countByRoleSQL = `
SELECT COUNT(*) FROM role_assignments WHERE role_id = $1`

	listByUserIDSQL = `
SELECT r.id, r.name, r.permissions, r.created_at
FROM roles r
JOIN role_assignments ra ON ra.role_id = r.id
WHERE ra.user_id = $1`
)

// PGRoleRepository implements ports.RoleRepository on PostgreSQL.
//
// Construction: error-first 2-param signature; nil checks fail-fast
// (PG-CONSTRUCTOR-MUST-FREE-01: no MustNew* variant is provided).
//
// TX semantics: ambient — if a pgx.Tx is stored in ctx under
// persistence.TxCtxKey (placed there by adapters/postgres.TxManager), each
// method joins it. Otherwise the call falls back to the pool. This is identical
// to the pattern in cells/accesscore/internal/adapters/postgres/user_repo.go.
//
// All CRUD methods are single-statement — no pool.Begin / BeginTx call is made
// (PG-REPO-AMBIENT-TX-01).
//
// AssignToUser handles 23505 and 23503 scenarios (see mapAssignError for details):
//   - (user_id, role_id) PK collision → absorbed by ON CONFLICT DO NOTHING (changed=false)
//   - idx_role_assignments_single_admin partial index conflict → ErrAuthRoleDuplicate
//   - fk_role_assignments_user violation → ErrAuthUserNotFound (migration 020)
//   - fk_role_assignments_role violation → ErrAuthRoleNotFound (migration 020)
type PGRoleRepository struct {
	pool *pgxpool.Pool
	clk  clock.Clock
}

// NewPGRoleRepository constructs a PGRoleRepository backed by the provided pool.
//
// Returns a non-nil error if pool or clk are nil (including typed-nil).
func NewPGRoleRepository(pool *pgxpool.Pool, clk clock.Clock) (*PGRoleRepository, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pg.NewPGRoleRepository: pool must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pg.NewPGRoleRepository: clock must not be nil")
	}
	return &PGRoleRepository{pool: pool, clk: clk}, nil
}

// execCtx executes a SQL statement against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGRoleRepository) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return r.pool.Exec(ctx, sql, args...)
}

// queryRowCtx queries a single row against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGRoleRepository) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

// queryCtx executes a multi-row query against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGRoleRepository) queryCtx(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Query(ctx, sql, args...)
	}
	return r.pool.Query(ctx, sql, args...)
}

// Create inserts a new role row.
//
// Returns ErrAuthRoleDuplicate (KindConflict) on UNIQUE 23505 violation
// (roles.name UNIQUE constraint).
func (r *PGRoleRepository) Create(ctx context.Context, role *domain.Role) error {
	permJSON, err := json.Marshal(permissionsToJSON(role.Permissions))
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: marshal permissions", err)
	}
	_, err = r.execCtx(ctx, insertRoleSQL,
		role.ID,
		role.Name,
		permJSON,
		r.clk.Now(),
	)
	if err != nil {
		return r.mapRoleCreateError(err, role.Name)
	}
	return nil
}

// mapRoleCreateError converts a raw pgx error from Create into an errcode.Error.
func (r *PGRoleRepository) mapRoleCreateError(err error, name string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		slog.Warn("role create: unique constraint violation",
			slog.String("constraint", pgErr.ConstraintName),
			slog.String("role_name", name),
		)
		// A9: ConstraintName is internal diagnostic — not exposed in 4xx body.
		return errcode.New(errcode.KindConflict, errcode.ErrAuthRoleDuplicate,
			"role name already exists",
			errcode.WithInternal("constraint="+pgErr.ConstraintName+" name="+name),
		)
	}
	return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: create", err)
}

// GetByID retrieves a role by primary key.
// Returns ErrAuthRoleNotFound (KindNotFound) when the row does not exist.
func (r *PGRoleRepository) GetByID(ctx context.Context, id string) (*domain.Role, error) {
	row := r.queryRowCtx(ctx, selectRoleByIDSQL, id)
	role, err := scanRole(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound,
				"role not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("id="+id),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: get by id", err)
	}
	return role, nil
}

// GetByUserID returns all roles assigned to the given user (unordered, no pagination).
// Returns an empty slice when the user holds no roles.
func (r *PGRoleRepository) GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error) {
	rows, err := r.queryCtx(ctx, listByUserIDSQL, userID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: get by user id", err)
	}
	defer rows.Close()

	var result []*domain.Role
	for rows.Next() {
		role, scanErr := scanRoleFromRows(rows)
		if scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: get by user id scan", scanErr)
		}
		result = append(result, role)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: get by user id rows", err)
	}
	if result == nil {
		return []*domain.Role{}, nil
	}
	return result, nil
}

// AssignToUser assigns the role to the user.
//
// Idempotent: if the user already holds the role, returns changed=false.
//
// 23505 scenarios:
//   - (user_id, role_id) PK collision — absorbed by ON CONFLICT DO NOTHING;
//     RowsAffected==0 → changed=false. No error returned.
//   - idx_role_assignments_single_admin partial index collision — NOT absorbed
//     by DO NOTHING (PG ON CONFLICT only matches the explicit conflict target).
//     Caught via pgErr.ConstraintName=="idx_role_assignments_single_admin",
//     mapped to ErrAuthRoleDuplicate (multi-pod concurrent assign).
//
// 23503 scenarios (migration 020 FK constraints):
//   - fk_role_assignments_user: userID does not exist → ErrAuthUserNotFound.
//   - fk_role_assignments_role: roleID does not exist → ErrAuthRoleNotFound.
func (r *PGRoleRepository) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	ct, err := r.execCtx(ctx, insertRoleAssignmentSQL, userID, roleID, r.clk.Now())
	if err != nil {
		return false, r.mapAssignError(err, userID, roleID)
	}
	return ct.RowsAffected() != 0, nil
}

// mapAssignError converts a raw pgx error from AssignToUser into an errcode.Error.
//
// 23505 (unique_violation):
//   - adminRoleConstraint → ErrAuthRoleDuplicate (single-admin race)
//   - other → unexpected infra error (maps to KindInternal)
//
// 23503 (foreign_key_violation, added by migration 020):
//   - fk_role_assignments_user → ErrAuthUserNotFound (non-existent user)
//   - fk_role_assignments_role → ErrAuthRoleNotFound (non-existent role)
func (r *PGRoleRepository) mapAssignError(err error, userID, roleID string) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: assign to user", err)
	}
	switch pgErr.Code {
	case pgUniqueViolation:
		if pgErr.ConstraintName == adminRoleConstraint {
			slog.Warn("role assign: single-admin constraint violation",
				slog.String("constraint", pgErr.ConstraintName),
				slog.String("role_id", roleID),
				slog.String("user_id", userID),
			)
			// A9: ConstraintName is internal diagnostic — not exposed in 4xx body.
			return errcode.New(errcode.KindConflict, errcode.ErrAuthRoleDuplicate,
				"admin role already assigned to another user",
				errcode.WithInternal(fmt.Sprintf("constraint=%s user_id=%s role_id=%s", pgErr.ConstraintName, userID, roleID)),
			)
		}
	case pgForeignKeyViolation:
		switch pgErr.ConstraintName {
		case "fk_role_assignments_user":
			return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
				"user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("user_id="+userID+" constraint="+pgErr.ConstraintName),
			)
		case "fk_role_assignments_role":
			return errcode.New(errcode.KindNotFound, errcode.ErrAuthRoleNotFound,
				"role not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("role_id="+roleID+" constraint="+pgErr.ConstraintName),
			)
		}
	}
	return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: assign to user", err)
}

// RemoveFromUser removes a role assignment.
// Idempotent: no error if the user does not hold the role.
func (r *PGRoleRepository) RemoveFromUser(ctx context.Context, userID, roleID string) error {
	_, err := r.execCtx(ctx, deleteRoleAssignmentSQL, userID, roleID)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: remove from user", err)
	}
	return nil
}

// RemoveFromUserIfNotLast atomically removes the role from the user only if at
// least one other holder will remain after the removal.
//
// Returns ErrAuthForbidden (KindPermissionDenied) when the user is the sole
// holder of the role. Returns changed=false when the user did not hold the role
// (idempotent no-op, matching mem.RoleRepository semantics).
//
// Atomicity: MUST be called within an ambient transaction (resolved via
// persistence.TxCtxKey). pg_advisory_xact_lock serializes concurrent revokes
// for the same role at the transaction level, so the lock+check+delete is
// fully atomic within the surrounding transaction. The lock is held until the
// transaction commits or rolls back. Without an ambient TX, the advisory lock
// is released immediately after the statement, which leaves a TOCTOU window
// between the COUNT check and the DELETE — this method fails fast in that case
// to enforce the documented contract rather than silently providing weaker
// isolation.
//
// ref: PostgreSQL pg_advisory_xact_lock docs
// ref: ory/keto internal/persistence/sql advisory_lock pattern
func (r *PGRoleRepository) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	if _, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); !ok {
		return false, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"role repo: RemoveFromUserIfNotLast requires ambient transaction",
			errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q", roleID, userID)),
		)
	}
	// Step 1: acquire the advisory lock as its OWN statement so the next
	// statement takes a fresh snapshot taken after the lock is held.
	// Embedding the lock in the CTE below would make the cnt clause read
	// the pre-lock snapshot — concurrent revokes would all see the original
	// holder count and all proceed to DELETE, defeating the lock.
	if _, err := r.execCtx(ctx, acquireRoleAdvisoryLockSQL, roleID); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: acquire advisory lock", err)
	}

	// Step 2: count + conditional delete. Now-fresh snapshot reflects any
	// committed deletes by sibling transactions that completed step 1 first.
	var (
		held    bool
		holders int64
		deleted int64
	)
	row := r.queryRowCtx(ctx, removeIfNotLastSQL, userID, roleID)
	if err := row.Scan(&held, &holders, &deleted); err != nil {
		return false, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: remove if not last", err)
	}

	if !held {
		// Idempotent no-op: user did not hold the role.
		return false, nil
	}
	if deleted == 1 {
		return true, nil
	}
	// held=true but deleted=0: this user is the sole holder.
	return false, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
		"cannot revoke role: this is the only holder; assign the role to another user first",
		errcode.WithInternal(fmt.Sprintf("role_id=%q user_id=%q holders=%d", roleID, userID, holders)),
	)
}

// CountByRole returns the number of users assigned to the given role.
func (r *PGRoleRepository) CountByRole(ctx context.Context, roleID string) (int, error) {
	var count int
	row := r.queryRowCtx(ctx, countByRoleSQL, roleID)
	if err := row.Scan(&count); err != nil {
		return 0, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: count by role", err)
	}
	return count, nil
}

// ListByUserID returns a paginated, sorted list of roles assigned to userID.
// Sorting and cursor-based pagination are applied in-memory after a full JOIN
// scan, matching the mem.RoleRepository approach. A future optimization can
// push ordering into SQL once the sort/cursor schema stabilizes.
func (r *PGRoleRepository) ListByUserID(ctx context.Context, userID string, params query.ListParams) ([]*domain.Role, error) {
	rows, err := r.queryCtx(ctx, listByUserIDSQL, userID)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: list by user id", err)
	}
	defer rows.Close()

	var all []*domain.Role
	for rows.Next() {
		role, scanErr := scanRoleFromRows(rows)
		if scanErr != nil {
			return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: list by user id scan", scanErr)
		}
		all = append(all, role)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "role repo: list by user id rows", err)
	}
	if all == nil {
		all = []*domain.Role{}
	}

	query.Sort(all, params.Sort, compareRoleField)
	result, err := query.ApplyCursor(all, params, roleFieldValue)
	if err != nil {
		return nil, fmt.Errorf("role repo: list-by-user cursor: %w", err)
	}
	return result, nil
}

// rowScanner is the common scan interface shared by pgx.Row and pgx.Rows.
// Used to deduplicate the scan logic between single-row and multi-row queries.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanRoleRow scans a rowScanner (pgx.Row or pgx.Rows) into a domain.Role.
func scanRoleRow(s rowScanner) (*domain.Role, error) {
	var (
		id        string
		name      string
		permJSON  []byte
		createdAt interface{}
	)
	if err := s.Scan(&id, &name, &permJSON, &createdAt); err != nil {
		return nil, err
	}
	return buildRole(id, name, permJSON)
}

// scanRole scans a pgx.Row into a domain.Role.
func scanRole(row pgx.Row) (*domain.Role, error) {
	return scanRoleRow(row)
}

// scanRoleFromRows scans a pgx.Rows (multi-row) into a domain.Role.
func scanRoleFromRows(rows pgx.Rows) (*domain.Role, error) {
	return scanRoleRow(rows)
}

// buildRole constructs a domain.Role from raw DB fields.
func buildRole(id, name string, permJSON []byte) (*domain.Role, error) {
	perms, err := permissionsFromJSON(permJSON)
	if err != nil {
		return nil, fmt.Errorf("role repo: unmarshal permissions for role %q: %w", id, err)
	}
	return &domain.Role{
		ID:          id,
		Name:        name,
		Permissions: perms,
	}, nil
}

// permissionJSON is the wire representation of a Permission for JSONB storage.
type permissionJSON struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

// permissionsToJSON converts domain.Permission slice to wire JSON slice.
func permissionsToJSON(perms []domain.Permission) []permissionJSON {
	if len(perms) == 0 {
		return []permissionJSON{}
	}
	out := make([]permissionJSON, len(perms))
	for i, p := range perms {
		out[i] = permissionJSON{Resource: p.Resource, Action: p.Action}
	}
	return out
}

// permissionsFromJSON parses JSONB bytes into domain.Permission slice.
func permissionsFromJSON(data []byte) ([]domain.Permission, error) {
	if len(data) == 0 || string(data) == "null" || string(data) == "[]" {
		return []domain.Permission{}, nil
	}
	var wire []permissionJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return nil, err
	}
	out := make([]domain.Permission, len(wire))
	for i, w := range wire {
		out[i] = domain.Permission{Resource: w.Resource, Action: w.Action}
	}
	return out, nil
}

// compareRoleField compares two roles by the given field name (used for in-memory sort).
func compareRoleField(a, b *domain.Role, field string) int {
	switch field {
	case "name":
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	case "id":
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// roleFieldValue returns the sortable field value for cursor encoding.
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
