package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
)

// SessionRepository persists and retrieves Session entities.
type SessionRepository interface {
	Create(ctx context.Context, session *domain.Session) error
	GetByID(ctx context.Context, id string) (*domain.Session, error)
	GetByRefreshToken(ctx context.Context, token string) (*domain.Session, error)
	// GetByPreviousRefreshToken looks up a session by its rotated-out refresh token.
	// Used for refresh token reuse detection: if a previously valid token is presented
	// again after rotation, the associated session should be revoked.
	GetByPreviousRefreshToken(ctx context.Context, token string) (*domain.Session, error)
	Update(ctx context.Context, session *domain.Session) error
	RevokeByUserID(ctx context.Context, userID string) error
	Delete(ctx context.Context, id string) error
}
