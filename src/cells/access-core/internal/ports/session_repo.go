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
	Update(ctx context.Context, session *domain.Session) error
	RevokeByUserID(ctx context.Context, userID string) error
	Delete(ctx context.Context, id string) error
}
