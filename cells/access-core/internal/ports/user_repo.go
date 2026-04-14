// Package ports defines the driven-side interfaces for access-core.
// Implementations live in adapters/ and are injected at assembly time.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
)

// UserRepository persists and retrieves User aggregates.
type UserRepository interface {
	Create(ctx context.Context, user *domain.User) error
	GetByID(ctx context.Context, id string) (*domain.User, error)
	GetByUsername(ctx context.Context, username string) (*domain.User, error)
	Update(ctx context.Context, user *domain.User) error
	Delete(ctx context.Context, id string) error
}
