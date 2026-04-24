package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
)

// SessionRepository persists and retrieves Session entities.
//
// Refresh-token lookup and reuse detection live in runtime/auth/refresh.Store
// (append-only lineage). SessionRepository is concerned only with
// access-token sessions and their revocation lifecycle.
type SessionRepository interface {
	Create(ctx context.Context, session *domain.Session) error
	GetByID(ctx context.Context, id string) (*domain.Session, error)
	Update(ctx context.Context, session *domain.Session) error
	// RevokeByIDAndOwner atomically revokes a session only if both id and
	// ownerUserID match. Returns ErrSessionNotFound when the session does not
	// exist OR does not belong to the caller — the two cases are intentionally
	// conflated to hide enumeration of other users' session ids.
	// ref: Ory Kratos session/handler.go deleteMySession — ownership enforced
	// as a WHERE clause on the persistence query rather than a handler-side
	// compare, eliminating the TOCTOU window that a load-then-check leaves.
	RevokeByIDAndOwner(ctx context.Context, id, ownerUserID string) error
	RevokeByUserID(ctx context.Context, userID string) error
	Delete(ctx context.Context, id string) error
}
