// Package ports defines the driven-side interfaces for accesscore.
// Implementations live in adapters/ and are injected at assembly time.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// UserRepository persists and retrieves User aggregates.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id string) (*domain.User, error)
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	// Update overwrites the mutable fields of an existing user. Reserved for
	// Lock / Unlock / UpdateProfile paths. Do NOT call this for password
	// changes — use UpdatePassword to get CAS-guarded semantics (S6).
	Update(ctx context.Context, user *domain.User) error
	Delete(ctx context.Context, id string) error

	// UpdatePassword applies a CAS-guarded password change.
	//
	// The SQL (or in-memory equivalent) is:
	//
	//	WHERE id=$userID AND password_version=$expectedPasswordVersion
	//
	// On version mismatch (0 rows affected) it returns ErrVersionConflict
	// (KindConflict / HTTP 409). On success it returns the new
	// password_version (= expectedPasswordVersion+1). Caller is responsible
	// for bcrypt-hashing newHash before passing it here.
	UpdatePassword(
		ctx context.Context,
		userID string,
		newHash string,
		resetRequired bool,
		expectedPasswordVersion int64,
	) (newVersion int64, err error)

	// BumpAuthzEpoch atomically increments users.authz_epoch by 1 and returns
	// the new value. It must be called inside an ambient transaction (the
	// credential-invalidation funnel entry point guarantees this). Returns
	// ErrAuthUserNotFound (KindNotFound) when no row matches userID.
	BumpAuthzEpoch(ctx context.Context, userID string) (newEpoch int64, err error)

	// GetByIDForUpdate fetches a user by primary key inside an ambient
	// transaction and acquires a row-level write lock (PG: SELECT ... FOR
	// UPDATE; mem: acquires store write mutex). Required by S4d sessionlogin
	// and authzmutate.Apply so that concurrent credential-invalidation
	// (Invalidator.Apply) cannot interleave between user read and downstream
	// session/refresh INSERT — without the row lock, login can mint tokens
	// with a snapshot of users.authz_epoch that the in-flight Invalidator has
	// already advanced (PR #490 review P1-#3).
	//
	// fail-fast enforced: both PG and mem implementations return
	// errcode.ErrInternal when called without an ambient transaction context.
	// PG checks for a pgx.Tx under persistence.TxCtxKey; mem checks for the
	// sentinel injected by Store.TxRunner().RunInTx.
	GetByIDForUpdate(ctx context.Context, id string) (*domain.User, error)

	// GetByUsernameForUpdate is the username-keyed counterpart to
	// GetByIDForUpdate. Used by sessionlogin (which dispatches by username);
	// callers from password / lock paths (which already have the userID) use
	// GetByIDForUpdate.
	//
	// fail-fast enforced: same contract as GetByIDForUpdate — both PG and mem
	// return errcode.ErrInternal when called outside an ambient transaction.
	GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error)
}
