// Package ports defines the driven-side interfaces for configcore.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
)

// ConfigRepository persists and retrieves ConfigEntry and ConfigVersion records.
type ConfigRepository interface {
	Create(ctx context.Context, entry *domain.ConfigEntry) error
	GetByKey(ctx context.Context, key string) (*domain.ConfigEntry, error)
	// Update atomically sets value and increments version if expectedVersion matches
	// the stored version (CAS guard). Preserves the existing sensitive flag — the
	// repo reads it internally via SELECT...FOR UPDATE to eliminate any TOCTOU race
	// on the sensitive flag. Returns ErrConfigRepoNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Update(ctx context.Context, key string, expectedVersion int, value string) (*domain.ConfigEntry, error)
	// UpdateForRollback atomically sets value AND sensitive, increments version,
	// provided expectedVersion matches the stored version (CAS guard).
	// Used exclusively by configpublish.Rollback to restore a snapshot's sensitivity
	// alongside its value. Returns ErrConfigRepoNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	UpdateForRollback(ctx context.Context, key string, expectedVersion int, value string, sensitive bool) (*domain.ConfigEntry, error)
	// Delete removes a config entry by key if expectedVersion matches the stored
	// version (CAS guard). Returns ErrConfigRepoNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Delete(ctx context.Context, key string, expectedVersion int) (*domain.ConfigEntry, error)
	List(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error)
	PublishVersion(ctx context.Context, version *domain.ConfigVersion) error
	GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error)
}
