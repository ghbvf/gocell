// Package ports defines the driven-side interfaces for config-core.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
)

// ConfigRepository persists and retrieves ConfigEntry and ConfigVersion records.
type ConfigRepository interface {
	Create(ctx context.Context, entry *domain.ConfigEntry) error
	GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error)
	Update(ctx context.Context, entry *domain.ConfigEntry) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context) ([]*domain.ConfigEntry, error)
	PublishVersion(ctx context.Context, version *domain.ConfigVersion) error
	GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error)
}
