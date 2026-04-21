// Package ports defines the driven-side interfaces for config-core.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
)

// ConfigRepository persists and retrieves ConfigEntry and ConfigVersion records.
type ConfigRepository interface {
	Create(ctx context.Context, entry *domain.ConfigEntry) error
	GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error)
	// Update modifies the config entry identified by key. The sensitive parameter
	// is caller-provided; the repo does NOT verify it against the current row value.
	// Callers must read the current sensitive flag before invoking Update (e.g.,
	// via GetByKey inside the same transaction) to avoid stale-flag overwrites.
	Update(ctx context.Context, key string, value string, sensitive bool) (*domain.ConfigEntry, error)
	Delete(ctx context.Context, key string) (*domain.ConfigEntry, error)
	List(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error)
	PublishVersion(ctx context.Context, version *domain.ConfigVersion) error
	GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error)
}
