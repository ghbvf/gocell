// Package ports defines the driven-side interfaces for accesscore.
// Implementations live in adapters/ and are injected at assembly time.
package ports

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/auth/credentialfence"
)

// UserRepository persists and retrieves User aggregates.
//
// # Write-method shape contract (USERREPO-METHOD-SET-FROZEN-01)
//
// Writes are split per use case so the column set each caller touches is
// expressed in method signature (Hard, type-system enforced; tools/archtest
// USERREPO-METHOD-SET-FROZEN-01 locks the method set):
//
//   - UpdateProfile           — username, email, updated_at
//   - UpdateLockState         — status, updated_at (+ lockout reset iff status=Active)
//   - UpdatePasswordResetFlag — password_reset_required, updated_at
//   - UpdatePassword          — password_hash, password_reset_required,
//     password_version (CAS), updated_at
//   - UpdateLockoutFields     — failed_login_count, last_failed_at, locked_until,
//     updated_at (auto-lockout path only)
//   - BumpAuthzEpoch          — authz_epoch (single-column atomic increment)
//
// Each method's godoc declares which columns it touches and which it does NOT.
// Adding a "generic Update(*domain.User)" back is rejected by archtest.
//
// ref: docs/architecture/202605222309-adr-user-repo-narrow-write-methods.md
// ref: github.com/ory/kratos/identity/pool.go UpdateIdentityColumns pattern
//
// # Tenancy (#1337 PR-2 + PR-3b, Model A)
//
// users.id is a global UUID primary key; username / email are unique only
// WITHIN a tenant (composite unique (tenant_id, username) / (tenant_id, email)).
// Every method takes a mandatory tenant.TenantID positional parameter
// immediately after ctx and applies `AND tenant_id = $N`. After PR-3b the
// tenant-less GetByID carve-out is removed; sessionrefresh derives tenant from
// session.ValidateView.TenantID (sessions.tenant_id carrier, migration 054).
type UserRepository interface {
	Create(ctx context.Context, t tenant.TenantID, user *domain.User) error
	// GetByIDInTenant fetches a user by primary key and requires it to belong to
	// tenant t. Returns ErrAuthUserNotFound when the row is absent OR belongs to a
	// different tenant — the caller cannot distinguish the two cases (no cross-tenant
	// existence leak). Validates t at the top (non-empty, canonical UUID).
	//
	// This is the only by-PK read path after PR-3b. The former tenant-less
	// GetByID carve-out (sessionrefresh) is replaced by deriving tenant from
	// session.ValidateView.TenantID (sessions.tenant_id carrier, migration 054).
	//
	// Row-visibility obligation (#1709, ROWSCOPE-REPO-PARAM-FUNNEL-01): vis is the
	// principal-derived owner-dimension obligation (owner column = users.id). The
	// subject-self read endpoint GET /api/v1/access/users/{id} passes the principal's
	// RowVisibility (RowScopeSelf for a normal user) so a non-admin who passes the
	// coarse route gate still only sees their own row — a non-self id IDOR-collapses
	// to ErrAuthUserNotFound. SYSTEM callers (login / refresh / validate / admin)
	// pass tenant.SystemRowVisibility() (RowScopeTenant, no owner predicate). vis is
	// validated fail-closed; RowScopeAll fail-closes with RowScopeAllUnsupportedError
	// (no cross-tenant accesscore read path).
	GetByIDInTenant(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, id string) (*domain.User, error)
	GetByUsername(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error)
	Delete(ctx context.Context, t tenant.TenantID, id string) error

	// UpdateProfile writes username / email / updated_at only.
	//
	// PATCH semantics: nil name/email skips that column (SQL COALESCE / mem
	// pointer-nil branch). Empty-string values are unrepresentable at the type
	// boundary — *domain.NonEmpty constructor (NewNonEmpty / UnmarshalJSON)
	// rejects "" so service-layer runtime checks are not required.
	//
	// Returns the post-write *domain.User reconstituted from the persisted
	// row (PG: RETURNING explicit user columns; mem: ReconstituteUser after
	// in-place write). The
	// returned aggregate is the new system-of-record value; caller MUST use
	// it for downstream publish / audit.
	//
	// Does NOT touch: password_hash / password_version / password_reset_required
	// / status / authz_epoch / failed_login_count / last_failed_at / locked_until.
	//
	// Errors:
	//   - ErrAuthUserNotFound (KindNotFound / 404) — userID does not exist
	//   - ErrAuthUserDuplicate (KindConflict / 409) — username or email collides
	//     with another row (unique constraint violation)
	//   - ErrInternal (KindInternal / 500) — other DB errors
	UpdateProfile(
		ctx context.Context,
		t tenant.TenantID,
		userID string,
		name, email *domain.NonEmpty,
		now time.Time,
	) (*domain.User, error)

	// UpdateLockState writes status + updated_at, with conditional auto-lockout
	// reset bound to the target status: status == StatusActive atomically zeros
	// failed_login_count / last_failed_at / locked_until in the same statement
	// (mem mirrors via ResetFailedLogins). This is a column-level invariant —
	// "activate without lockout reset" is not expressible at the call site,
	// closing the PR #585 P1#3 admin-unlock re-lock race at the schema layer.
	//
	// Touches: status, updated_at, and (iff status == StatusActive) the three
	// auto-lockout columns.
	//
	// Does NOT touch: username / email / password_hash / password_version /
	// password_reset_required / authz_epoch.
	//
	// Effective-admin guard runs at the persistence boundary: PG migration 024
	// effective_admin_invariant_on_users BEFORE UPDATE trigger; mem
	// guardEffectiveAdminRemovalLocked. Returns ErrAuthLastAdminProtected
	// (KindPermissionDenied / 403) when the change would demote the sole
	// effective admin.
	//
	// Returns only error (not the updated aggregate); callers that need the
	// post-write User state should call GetByIDInTenant after the mutation.
	//
	// Errors:
	//   - ErrAuthUserNotFound (KindNotFound / 404) — userID does not exist
	//   - ErrAuthLastAdminProtected (KindPermissionDenied / 403) — last admin
	//   - ErrInternal (KindInternal / 500) — other DB errors
	UpdateLockState(
		ctx context.Context,
		t tenant.TenantID,
		userID string,
		status domain.UserStatus,
		now time.Time,
	) error

	// UpdatePasswordResetFlag writes password_reset_required + updated_at only.
	//
	// Does NOT touch: username / email / password_hash / password_version /
	// status / authz_epoch / failed_login_count / last_failed_at / locked_until.
	//
	// Returns only error (not the updated aggregate); callers that need the
	// post-write User state should call GetByIDInTenant after the mutation.
	//
	// Errors:
	//   - ErrAuthUserNotFound (KindNotFound / 404) — userID does not exist
	//   - ErrInternal (KindInternal / 500) — other DB errors
	UpdatePasswordResetFlag(
		ctx context.Context,
		t tenant.TenantID,
		userID string,
		required bool,
		now time.Time,
	) error

	// UpdatePassword applies a CAS-guarded password change, gated on the account
	// being active at write time.
	//
	// The SQL (or in-memory equivalent) is:
	//
	//	WHERE id=$userID AND password_version=$expectedPasswordVersion AND status='active'
	//
	// The status='active' predicate (#1017 F1) is the write-time backstop for a
	// concurrent Lock/Suspend committing between the caller's read and this write:
	// a now-frozen account's credential MUST NOT be rewritten. On 0 rows affected
	// the implementation re-reads the row to disambiguate the cause:
	//
	//   - row absent              → ErrAuthUserNotFound   (KindNotFound / 404)
	//   - status != active        → ErrAuthUserNotActive  (KindPermissionDenied / 403)
	//   - version mismatch (active) → ErrVersionConflict  (KindConflict / 409)
	//
	// Inactive is checked before version so a concurrent freeze is reported as
	// 403 even if a concurrent change also advanced the version. On success it
	// returns the new password_version (= expectedPasswordVersion+1). Caller is
	// responsible for bcrypt-hashing newHash before passing it here.
	UpdatePassword(
		ctx context.Context,
		t tenant.TenantID,
		userID string,
		newHash string,
		resetRequired bool,
		expectedPasswordVersion int64,
	) (newVersion int64, err error)

	// BumpAuthzEpoch atomically increments users.authz_epoch by 1 and returns
	// the new value. It must be called inside an ambient transaction (the
	// credential-invalidation funnel entry point guarantees this). Returns
	// ErrAuthUserNotFound (KindNotFound) when no row matches userID.
	//
	// tok is a capability proof that this call routes through
	// credentialinvalidate.Invalidator via credentialfence.Mint; only
	// credentialinvalidate may mint a non-nil token in production. Passing a
	// nil token panics through the panicregister funnel (B-class programmer
	// error) at the implementation boundary.
	BumpAuthzEpoch(ctx context.Context, t tenant.TenantID, userID string, tok credentialfence.FenceToken) (newEpoch int64, err error)

	// GetByIDForUpdate fetches a user by primary key inside an ambient
	// transaction and acquires a row-level write lock (PG: SELECT ... FOR
	// UPDATE; mem: acquires store write mutex). Required by S4d sessionlogin
	// and authzmutate.Apply so that concurrent credential-invalidation
	// (Invalidator.Apply) cannot interleave between user read and downstream
	// session/refresh INSERT — without the row lock, login can mint tokens
	// with a snapshot of users.authz_epoch that the in-flight Invalidator has
	// already advanced (PR #490 review P1-#3).
	//
	// Serialization contract differs by implementation:
	//   - PG: fail-fast — returns errcode.ErrInternal without a pgx.Tx under
	//     persistence.TxCtxKey (FOR UPDATE is meaningless outside a tx). This
	//     is the production hard guarantee.
	//   - mem: under the Store's own TxRunner the tx holds store.mu for the
	//     whole closure → full FOR-UPDATE-until-commit serialization. Driven
	//     by a foreign CellTxManager (corebundle PG-outbox topology,
	//     ssobff/demo) it takes store.mu per call — functional; cross-statement
	//     serialization holds only on the Store-TxRunner path. mem never
	//     hard-fails on TxRunner pairing (that broke real composition roots, #501).
	GetByIDForUpdate(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error)

	// GetByUsernameForUpdate is the username-keyed counterpart to
	// GetByIDForUpdate. Used by sessionlogin (which dispatches by username);
	// callers from password / lock paths (which already have the userID) use
	// GetByIDForUpdate.
	//
	// Serialization contract: same as GetByIDForUpdate (PG fail-fast hard
	// guarantee; mem full serialization on Store-TxRunner, per-call lock under
	// a foreign CellTxManager, never hard-fails on pairing).
	GetByUsernameForUpdate(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error)

	// UpdateLockoutFields persists the auto-lockout state (failed_login_count,
	// last_failed_at, locked_until) for an existing user. Called exclusively
	// from corecells/accesscore/internal/accountlockout. The status / authz_epoch
	// / password_hash columns are NOT touched by this path — status
	// transitions go through UpdateLockState, which itself zeros the lockout
	// columns when status == Active (atomic via SQL CASE / mem mirror).
	//
	// Must be invoked within an ambient transaction (txCtx) so the counter
	// update co-commits with the surrounding sessionlogin tx (L2 OutboxFact).
	// Returns ErrAuthUserNotFound (KindNotFound) when no row matches user.ID.
	UpdateLockoutFields(ctx context.Context, t tenant.TenantID, user *domain.User) error
}
