package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres/internal/pgexec"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	pgquery "github.com/ghbvf/gocell/pkg/pgquery"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// Compile-time assertion: PGUserRepo implements ports.UserRepository.
var _ ports.UserRepository = (*PGUserRepo)(nil)

const msgUserInvalidTenant = "user_repo: invalid tenant"

// PGUserRepo is the cell-private PostgreSQL implementation of ports.UserRepository.
// It reads/writes the `users` table (migration 017).
//
// Transaction contract — dual-signal pattern (S3+S5 PR #449 round-3
// clarification). The txRunner field is a *construction-time policy
// declaration*: it fail-fasts at NewPGUserRepo when the L2 caller has not
// wired a real TxRunner (single source of truth for "this repo is intended
// for L2-atomic call sites"). The repo methods themselves do NOT invoke
// txRunner.RunInTx directly because all current write paths are
// single-statement (Create / Update / Delete). Instead, methods extract any
// ambient pgx.Tx from ctx via kernel/persistence.TxCtxKey (the value stored
// by adapters/postgres.TxManager.RunInTx); when no tx is in ctx the methods
// fall through to the pool. The setup service wraps Create + outbox.Write
// in a single TxManager.RunInTx call so both writes share the tx that the
// package-local typed executor picks up here.
//
// Compare runtime/auth/refresh adapters/postgres/refresh_store.go where
// txRunner IS invoked directly because its multi-statement methods (Peek,
// Rotate) need an explicit boundary. PGUserRepo's pattern is the
// "single-statement repo" variant of the same dual-signal contract.
type PGUserRepo struct {
	db pgexec.PGExecutor
	// txRunner is retained at construction time as policy declaration only —
	// see the type godoc above. Repo methods read tx from ctx, not this field.
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewPGUserRepo constructs a PGUserRepo. Fails fast on nil dependencies.
func NewPGUserRepo(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	clk clock.Clock,
) (*PGUserRepo, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGUserRepo: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGUserRepo: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGUserRepo: clock must not be nil")
	}
	return &PGUserRepo{
		db:       pgexec.New(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

// msgUserNotFound is the errcode message for user-not-found errors (8 sites).
const msgUserNotFound = "user not found"

const (
	// S4d: authz_epoch is sourced from the domain.User (NewUser sets it to 1
	// — the unset sentinel is 0, which session/refresh stores reject).
	// Bumping is still exclusive to UpdateAuthzEpoch / BumpAuthzEpoch; this
	// INSERT only seeds the initial value from the in-memory aggregate.
	// $1=tenant_id, $2=id, ..., $11=updated_at.
	insertUserSQL = `
INSERT INTO users (
    tenant_id, id, username, email, password_hash, password_reset_required,
    status, creation_source, authz_epoch, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`

	// selectUserByIDInTenantSQL: $1=id, $2=tenant_id — tenant-scoped by-PK read.
	// Collapses "row not in this tenant" and "row absent" into a single
	// pgx.ErrNoRows so no cross-tenant existence leaks.
	selectUserByIDInTenantSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at,
       failed_login_count, last_failed_at, locked_until, tenant_id
FROM users
WHERE id = $1 AND tenant_id = $2`

	// selectUserByUsernameSQL: $1=tenant_id, $2=username.
	selectUserByUsernameSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at,
       failed_login_count, last_failed_at, locked_until, tenant_id
FROM users
WHERE tenant_id = $1 AND username = $2`

	// selectUserByIDForUpdateSQL: $1=tenant_id, $2=id, FOR UPDATE. Tenant-scoped
	// (NOT the GetByID carve-out): GetByIDForUpdate callers are post-auth and
	// carry a tenant, so the for-update read must apply the tenant predicate —
	// otherwise a tenant-A admin's FOR UPDATE read would surface a tenant-B row
	// before the tenant-scoped UPDATE rejects it (cross-tenant read leak).
	// selectUserByUsernameForUpdateSQL: $1=tenant_id, $2=username, FOR UPDATE.
	selectUserByIDForUpdateSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at,
       failed_login_count, last_failed_at, locked_until, tenant_id
FROM users
WHERE tenant_id = $1 AND id = $2
FOR UPDATE`

	// $1=tenant_id, $2=username.
	selectUserByUsernameForUpdateSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at,
       failed_login_count, last_failed_at, locked_until, tenant_id
FROM users
WHERE tenant_id = $1 AND username = $2
FOR UPDATE`

	// deleteUserSQL: $1=tenant_id, $2=id.
	deleteUserSQL = `DELETE FROM users WHERE tenant_id = $1 AND id = $2`

	// updateProfileSQL: $1=id, $2=username (nullable), $3=email (nullable), $4=now, $5=tenant_id.
	updateProfileSQL = `
UPDATE users
SET username   = COALESCE($2, username),
    email      = COALESCE($3, email),
    updated_at = $4
WHERE tenant_id = $5 AND id = $1
RETURNING id, username, email, password_hash, password_version, password_reset_required,
          status, creation_source, authz_epoch, created_at, updated_at,
          failed_login_count, last_failed_at, locked_until, tenant_id`

	// updateLockStateSQL: $1=id, $2=status, $3=now, $4=tenant_id.
	updateLockStateSQL = `
UPDATE users
SET status             = $2,
    updated_at         = $3,
    failed_login_count = CASE WHEN $2 = 'active' THEN 0    ELSE failed_login_count END,
    last_failed_at     = CASE WHEN $2 = 'active' THEN NULL ELSE last_failed_at     END,
    locked_until       = CASE WHEN $2 = 'active' THEN NULL ELSE locked_until       END
WHERE tenant_id = $4 AND id = $1`

	// updatePasswordResetFlagSQL: $1=id, $2=required, $3=now, $4=tenant_id
	//nolint:gosec // G101: SQL constant containing "password" column name, not a credential value
	updatePasswordResetFlagSQL = `
UPDATE users
SET password_reset_required = $2,
    updated_at              = $3
WHERE tenant_id = $4 AND id = $1`

	// maxFailedLoginCount caps the in-domain failed_login_count value before
	// it crosses the PG int32 wire boundary (column type is INTEGER in
	// migration 032). Real-world threshold = 5 and the counter resets on
	// stale-window / lazy-unlock / success, so this guard is a defensive
	// upper bound rather than an operational limit.
	//
	// 1<<30 (≈ 1.07e9) is chosen instead of math.MaxInt32 (≈ 2.14e9) so the
	// cap is clearly distinguishable from a "real" counter in forensic
	// queries — any row with failed_login_count >= 1<<30 is unambiguously
	// a data-integrity signal (corrupted aggregate state, etc.), not a
	// legitimate counter value. The 2x headroom under int32 also leaves
	// room for a future migration to widen the column without revisiting
	// this constant.
	maxFailedLoginCount = 1 << 30

	// bumpAuthzEpochSQL: $1=id, $2=tenant_id — tenant-scoped to prevent a
	// cross-tenant authz_epoch bump if an attacker presents a valid UUID from
	// another tenant. mem applies the same tenant predicate via userByIDInTenant.
	bumpAuthzEpochSQL = `UPDATE users SET authz_epoch = authz_epoch + 1 WHERE id = $1 AND tenant_id = $2 RETURNING authz_epoch`

	// updateLockoutFieldsSQL: $1=id, $2=count, $3=last_failed_at, $4=locked_until, $5=updated_at, $6=tenant_id.
	updateLockoutFieldsSQL = `
UPDATE users
SET failed_login_count = $2,
    last_failed_at = $3,
    locked_until = $4,
    updated_at = $5
WHERE tenant_id = $6 AND id = $1`

	// updatePasswordSQL: $1=newHash, $2=resetRequired, $3=now, $4=id, $5=expectedPV, $6=tenant_id
	//nolint:gosec // G101: SQL constant containing "password" column name, not a credential value
	updatePasswordSQL = `
UPDATE users
SET password_hash = $1,
    password_reset_required = $2,
    password_version = password_version + 1,
    updated_at = $3
WHERE tenant_id = $6 AND id = $4 AND password_version = $5 AND status = 'active'
RETURNING password_version`
)

// validateFailedLoginCount is the shared range guard for the auto-lockout
// counter at the PG wire boundary. Update and UpdateLockoutFields both call
// it before passing the int32 value to pgx; the in-domain counter is
// expected to satisfy 0 <= count <= maxFailedLoginCount, so a violation
// here signals corrupt aggregate state and is returned as KindInternal
// (operators should see a 5xx, not a silent overflow / wrap).
//
// Returns the bounds-checked int32 value alongside any error so callers can
// pass the result straight into pgx without a second int32 conversion at
// the call site (which would trip gosec G115 — the bounds check moved into
// the helper, so the conversion must happen here too).
func validateFailedLoginCount(userID string, count int) (int32, error) {
	if count < 0 || int64(count) > int64(maxFailedLoginCount) {
		return 0, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"user_repo: failed_login_count out of range",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s count=%d", userID, count))))
	}
	// G115 bounds-check is performed above; the int32 conversion is safe.
	return int32(count), nil
}

// Create inserts a new user row. Returns ErrAuthUserDuplicate on unique
// constraint violation (username or email already taken within the tenant).
func (r *PGUserRepo) Create(ctx context.Context, t tenant.TenantID, user *domain.User) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	_, err := r.db.Exec(
		ctx, insertUserSQL,
		string(t),
		user.ID,
		user.Username,
		user.Email,
		user.PasswordHash,
		user.PasswordResetRequired(),
		string(user.Status()),
		string(user.CreationSource),
		user.AuthzEpoch(),
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		if pgquery.IsUniqueViolation(err) {
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
				"username or email already exists",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("username=%q email=%q", user.Username, user.Email))))
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: create", err)
	}
	return nil
}

// GetByIDInTenant fetches a user by primary key within tenant t. Returns
// ErrAuthUserNotFound when the row is absent OR belongs to a different tenant —
// both cases produce pgx.ErrNoRows from `WHERE id=$1 AND tenant_id=$2`.
func (r *PGUserRepo) GetByIDInTenant(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	row := r.db.QueryRow(ctx, selectUserByIDInTenantSQL, id, string(t))
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", id))))
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-id-in-tenant", err)
	}
	return u, nil
}

// GetByUsername fetches a user by (tenant_id, username). Returns ErrAuthUserNotFound when absent.
func (r *PGUserRepo) GetByUsername(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	row := r.db.QueryRow(ctx, selectUserByUsernameSQL, string(t), username)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("username=%q", username))))
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-username", err)
	}
	return u, nil
}

// userLookupKind selects the lookup column for the GetByXxxForUpdate family.
// Adding a new lookup dimension (e.g., email) requires (a) a new const here,
// (b) a corresponding SQL constant, (c) two switch cases in getForUpdateBy
// (SQL/attr part + errcode.Wrap message). The switch-based dispatch keeps each
// errcode.Wrap callsite a const string literal at AST level, satisfying
// MESSAGE-CONST-LITERAL-01 archtest without a //nolint:dupl carve-out.
type userLookupKind int

const (
	lookupByID userLookupKind = iota
	lookupByUsername
)

// getForUpdateBy is the shared body of GetByIDForUpdate / GetByUsernameForUpdate.
// (S4d) Row lock via SELECT ... FOR UPDATE. Fail-fast on missing ambient tx;
// the lock guarantee cannot silently degrade. Each errcode.Wrap/New callsite
// receives a const string literal (MESSAGE-CONST-LITERAL-01 compliant).
// t scopes BOTH lookups: the username lookup (composite unique) and the ID
// lookup (post-auth callers carry a tenant, so the for-update read is
// tenant-scoped to avoid a cross-tenant read leak — see selectUserByIDForUpdateSQL).
func (r *PGUserRepo) getForUpdateBy(
	ctx context.Context, t tenant.TenantID, kind userLookupKind, value string,
) (*domain.User, error) {
	if err := assertAmbientTx(ctx); err != nil {
		return nil, err
	}
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	var (
		sqlStr   string
		attrPart string
		args     []any
	)
	switch kind {
	case lookupByID:
		// $1=tenant_id, $2=id — tenant-scoped FOR UPDATE read.
		sqlStr = selectUserByIDForUpdateSQL
		attrPart = fmt.Sprintf("id=%s", value)
		args = []any{string(t), value}
	case lookupByUsername:
		// username is arbitrary user-supplied text — %q quotes the value so
		// embedded whitespace / special chars stay parseable in slog Internal.
		sqlStr = selectUserByUsernameForUpdateSQL
		attrPart = fmt.Sprintf("username=%q", value)
		args = []any{string(t), value}
	default:
		// Fail-fast before any DB roundtrip. Unknown kind is a programmer
		// error (new const added without extending this switch); panic-
		// registered A class per PANIC-REGISTERED-01 surfaces it immediately
		// at runtime instead of routing an empty SQL string to the database.
		panic(panicregister.Approved("user-repo-lookup-kind-unreachable",
			errcode.Assertion("user_repo: unexpected userLookupKind in getForUpdateBy")))
	}
	row := r.db.QueryRow(ctx, sqlStr, args...)
	u, err := scanUser(row)
	if err == nil {
		return u, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", attrPart)))
	}
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
		return nil, err
	}
	switch kind {
	case lookupByID:
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-id-for-update", err)
	case lookupByUsername:
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-username-for-update", err)
	default:
		// Symmetric defense to the first switch's default — already covered
		// there, but Go's return-exhaustiveness analyzer requires a terminal
		// statement here.
		panic(panicregister.Approved("user-repo-lookup-kind-unreachable",
			errcode.Assertion("user_repo: unexpected userLookupKind in getForUpdateBy")))
	}
}

// GetByIDForUpdate (S4d) — see ports.UserRepository godoc. Acquires a row
// lock via SELECT ... FOR UPDATE. The lookup is tenant-scoped (WHERE tenant_id=$1
// AND id=$2) to prevent a cross-tenant read leak on the FOR UPDATE path.
// Tenant validation is performed inside getForUpdateBy (the shared convergence
// point for both lookup kinds); no redundant Validate call here.
func (r *PGUserRepo) GetByIDForUpdate(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	return r.getForUpdateBy(ctx, t, lookupByID, id)
}

// GetByUsernameForUpdate (S4d) — see ports.UserRepository godoc. Tenant
// validation is performed inside getForUpdateBy; no redundant Validate call here.
func (r *PGUserRepo) GetByUsernameForUpdate(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error) {
	return r.getForUpdateBy(ctx, t, lookupByUsername, username)
}

// UpdateProfile writes username / email / updated_at. Nil name or email leaves
// that column unchanged (SQL COALESCE). Returns the post-write *domain.User
// reconstituted from the RETURNING row (explicit user column list); caller
// MUST use it as the new system-of-record aggregate.
func (r *PGUserRepo) UpdateProfile(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	name, email *domain.NonEmpty,
	now time.Time,
) (*domain.User, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	// pgx binds *string (PG TEXT) directly; *domain.NonEmpty is type-renamed
	// string but pgx's reflect path treats it as plain text. We convert to
	// *string for clarity and to keep the wire-binding contract explicit.
	var namePG, emailPG *string
	if name != nil {
		s := string(*name)
		namePG = &s
	}
	if email != nil {
		s := string(*email)
		emailPG = &s
	}
	// updateProfileSQL: $1=id, $2=username, $3=email, $4=now, $5=tenant_id
	row := r.db.QueryRow(ctx, updateProfileSQL, userID, namePG, emailPG, now, string(t))
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", userID))))
		}
		if pgquery.IsUniqueViolation(err) {
			return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
				"username or email already exists",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", userID))))
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update profile", err)
	}
	return u, nil
}

// UpdateLockState writes status + updated_at, atomically resetting the three
// auto-lockout columns when status == StatusActive (SQL CASE clause). Returns
// ErrAuthLastAdminProtected when the migration-024 trigger blocks the change.
func (r *PGUserRepo) UpdateLockState(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	status domain.UserStatus,
	now time.Time,
) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	// updateLockStateSQL: $1=id, $2=status, $3=now, $4=tenant_id
	tag, err := r.db.Exec(ctx, updateLockStateSQL, userID, string(status), now, string(t))
	if err != nil {
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last effective admin",
				errcode.WithCategory(errcode.CategoryAuth),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s status=%q", userID, string(status)))))
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update lock state", err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", userID))))
	}
	return nil
}

// UpdatePasswordResetFlag writes password_reset_required + updated_at only.
func (r *PGUserRepo) UpdatePasswordResetFlag(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	required bool,
	now time.Time,
) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	// updatePasswordResetFlagSQL: $1=id, $2=required, $3=now, $4=tenant_id
	tag, err := r.db.Exec(ctx, updatePasswordResetFlagSQL, userID, required, now, string(t))
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update password reset flag", err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", userID))))
	}
	return nil
}

// Delete removes a user row. Returns ErrAuthUserNotFound when no row matched.
// Returns ErrAuthLastAdminProtected (403) when the migration-024 trigger on
// `users` rejects the delete because the row is the sole effective admin —
// same errcode + message as PGUserRepo.Update / domain.LastAdminGuard so
// client handlers match a single business invariant regardless of which
// layer caught the violation.
func (r *PGUserRepo) Delete(ctx context.Context, t tenant.TenantID, id string) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	// deleteUserSQL: $1=tenant_id, $2=id
	tag, err := r.db.Exec(ctx, deleteUserSQL, string(t), id)
	if err != nil {
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last effective admin",
				errcode.WithCategory(errcode.CategoryAuth),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("user_id=%s", id))))
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: delete", err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", id))))
	}
	return nil
}

// BumpAuthzEpoch atomically increments users.authz_epoch by 1 and returns the
// new value. It must be called inside an ambient transaction — the
// credential-invalidation funnel entry point guarantees this. Returns
// ErrAuthUserNotFound (KindNotFound) when no row matches userID.
//
// fail-fast enforced: calling without an ambient transaction returns an error
// (errcode.ErrInternal); without a transaction the row update is auto-committed
// before the caller's surrounding atomic sequence completes.
func (r *PGUserRepo) BumpAuthzEpoch(ctx context.Context, t tenant.TenantID, userID string, tok credentialfence.FenceToken) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	credentialfence.MustHave(tok, "ports.UserRepository.BumpAuthzEpoch")
	if err := assertAmbientTx(ctx); err != nil {
		return 0, err
	}
	var newEpoch int64
	// bumpAuthzEpochSQL: $1=id, $2=tenant_id — tenant-scoped; see SQL constant.
	err := r.db.QueryRow(ctx, bumpAuthzEpochSQL, userID, string(t)).Scan(&newEpoch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", userID))))
		}
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: bump authz epoch", err)
	}
	return newEpoch, nil
}

// scanUser scans a single Row into a domain.User via domain.ReconstituteUser.
// Column order must match selectUserByIDSQL and selectUserByUsernameSQL.
//
// authz_epoch is included so that sessionvalidate's epoch invariant
// (user.AuthzEpoch != claims.AuthzEpoch → 401) sees the post-bump value;
// omitting it silently leaves AuthzEpoch=0 on every read and breaks the
// credential-invalidation chain (Finding #1 / PR #490 review).
func scanUser(row pgx.Row) (*domain.User, error) {
	var (
		id, username, email, passwordHash string
		passwordVersion                   int64
		passwordResetRequired             bool
		status, source                    string
		authzEpoch                        int64
		createdAt, updatedAt              time.Time
		failedLoginCount                  int32
		lastFailedAt, lockedUntil         *time.Time
		tenantID                          string
	)
	err := row.Scan(
		&id,
		&username,
		&email,
		&passwordHash,
		&passwordVersion,
		&passwordResetRequired,
		&status,
		&source,
		&authzEpoch,
		&createdAt,
		&updatedAt,
		&failedLoginCount,
		&lastFailedAt,
		&lockedUntil,
		&tenantID,
	)
	if err != nil {
		return nil, err
	}
	if !domain.ValidUserStatus(domain.UserStatus(status)) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"scanUser: invalid status from DB",
			errcode.WithDetails(errcode.PublicString("table", "users"), errcode.PublicString("column", "status")),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("scanned status=%q", status))))
	}
	if !domain.ValidUserSource(domain.UserSource(source)) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"scanUser: invalid creation_source from DB",
			errcode.WithDetails(errcode.PublicString("table", "users"), errcode.PublicString("column", "creation_source")),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("scanned source=%q", source))))
	}
	if failedLoginCount < 0 {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"scanUser: failed_login_count must be >= 0",
			errcode.WithDetails(errcode.PublicString("table", "users"), errcode.PublicString("column", "failed_login_count")),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("scanned value=%d", failedLoginCount))))
	}
	u, reconErr := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:                    id,
		TenantID:              tenant.TenantID(tenantID),
		Username:              username,
		Email:                 email,
		PasswordHash:          passwordHash,
		PasswordVersion:       passwordVersion,
		PasswordResetRequired: passwordResetRequired,
		Status:                domain.UserStatus(status),
		Source:                domain.UserSource(source),
		AuthzEpoch:            authzEpoch,
		CreatedAt:             createdAt,
		UpdatedAt:             updatedAt,
		FailedLoginCount:      int(failedLoginCount),
		LastFailedAt:          lastFailedAt,
		LockedUntil:           lockedUntil,
	})
	if reconErr != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"scanUser: ReconstituteUser failed", reconErr)
	}
	return u, nil
}

// UpdateLockoutFields persists the auto-lockout state (failed_login_count,
// last_failed_at, locked_until, updated_at) for an existing user. Called
// exclusively from cells/accesscore/internal/accountlockout inside the
// sessionlogin tx. Returns ErrAuthUserNotFound when no row matched.
//
// Schema note: this method touches only the four lockout-bookkeeping
// columns plus updated_at. The status / authz_epoch / password_hash columns
// remain owned by authzmutate.Mutator.ApplyInTx (Update / BumpAuthzEpoch) and
// the password-change path (UpdatePassword); they are NOT mutated here.
func (r *PGUserRepo) UpdateLockoutFields(ctx context.Context, t tenant.TenantID, user *domain.User) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	count32, err := validateFailedLoginCount(user.ID, user.FailedLoginCount())
	if err != nil {
		return err
	}
	// updateLockoutFieldsSQL: $1=id, $2=count, $3=last_failed_at, $4=locked_until, $5=updated_at, $6=tenant_id.
	tag, err := r.db.Exec(
		ctx, updateLockoutFieldsSQL,
		user.ID,
		count32,
		user.LastFailedAt(),
		user.AutoLockoutDeadline(),
		user.UpdatedAt,
		string(t),
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update lockout fields", err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, msgUserNotFound,
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", user.ID))))
	}
	return nil
}

// UpdatePassword applies a CAS-guarded password write.
//
// It executes updatePasswordSQL (WHERE id=$4 AND password_version=$5). If 0
// rows were affected the method distinguishes "user absent" from "version
// mismatch" via a follow-up GetByID probe — callers receive ErrAuthUserNotFound
// or ErrVersionConflict respectively. On success the new password_version is
// returned.
func (r *PGUserRepo) UpdatePassword(
	ctx context.Context,
	t tenant.TenantID,
	userID string,
	newHash string,
	resetRequired bool,
	expectedPV int64,
) (int64, error) {
	if err := t.Validate(); err != nil {
		return 0, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgUserInvalidTenant, err)
	}
	now := r.clock.Now()
	var newPV int64
	// updatePasswordSQL: $1=newHash, $2=resetRequired, $3=now, $4=id, $5=expectedPV, $6=tenant_id
	err := r.db.QueryRow(
		ctx, updatePasswordSQL,
		newHash, resetRequired, now, userID, expectedPV, string(t),
	).Scan(&newPV)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// 0 rows: re-read to disambiguate absent / inactive / version mismatch
			// (the WHERE clause guards tenant_id, id, status='active', and password_version).
			// Tenant-scoped re-read: a cross-tenant id must collapse to
			// ErrAuthUserNotFound (IDOR-safe), not leak a version conflict — the
			// tenant-less GetByID would find the foreign-tenant row and misreport
			// a CAS conflict.
			cur, gerr := r.GetByIDInTenant(ctx, t, userID)
			if gerr != nil {
				return 0, gerr // user absent in this tenant (or infra error)
			}
			// Inactive before version (#1017 F1): a concurrent freeze is a 403,
			// even if the version also advanced.
			if cur.Status() != domain.StatusActive {
				return 0, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
					"account is not active",
					errcode.WithCategory(errcode.CategoryDomain),
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%s", userID))))
			}
			// Row active but version didn't match — CAS conflict.
			return 0, cas.CheckVersionMatch(0, "user", userID)
		}
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update password", err)
	}
	return newPV, nil
}
