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
	// Update atomically sets value and increments version. Preserves the existing
	// sensitive flag — the repo reads it internally via SELECT...FOR UPDATE to
	// eliminate any TOCTOU race on the sensitive flag. Callers do not need to
	// pre-read the entry. Returns ErrConfigRepoNotFound if the key does not exist.
	Update(ctx context.Context, key string, value string) (*domain.ConfigEntry, error)
	// UpdateForRollback atomically sets value AND sensitive, increments version.
	// Used exclusively by configpublish.Rollback to restore a snapshot's sensitivity
	// alongside its value. Returns ErrConfigRepoNotFound if the key does not exist.
	// TODO(505-followup): add WHERE version=$expected for optimistic locking;
	// return ErrConfigVersionConflict (409) on mismatch.
	UpdateForRollback(ctx context.Context, key string, value string, sensitive bool) (*domain.ConfigEntry, error)
	Delete(ctx context.Context, key string) (*domain.ConfigEntry, error)
	List(ctx context.Context, params query.ListParams) ([]*domain.ConfigEntry, error)
	PublishVersion(ctx context.Context, version *domain.ConfigVersion) error
	GetVersion(ctx context.Context, configID string, version int) (*domain.ConfigVersion, error)
}
