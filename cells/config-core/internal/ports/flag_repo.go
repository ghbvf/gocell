package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
)

// FlagRepository persists and retrieves FeatureFlag records.
type FlagRepository interface {
	Create(ctx context.Context, flag *domain.FeatureFlag) error
	GetByKey(ctx context.Context, key string) (*domain.FeatureFlag, error)
	Update(ctx context.Context, flag *domain.FeatureFlag) error
	List(ctx context.Context, params query.ListParams) ([]*domain.FeatureFlag, error)
	// Delete removes a feature flag by key. Returns ErrFlagRepoNotFound if the
	// key does not exist.
	Delete(ctx context.Context, key string) error
	// Toggle sets the enabled state of a feature flag atomically, incrementing
	// version by 1. It does not overwrite rollout_percentage or description.
	// Returns the updated flag. Returns ErrFlagRepoNotFound if the key does not exist.
	Toggle(ctx context.Context, key string, enabled bool) (*domain.FeatureFlag, error)
}
