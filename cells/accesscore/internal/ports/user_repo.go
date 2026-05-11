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
}
