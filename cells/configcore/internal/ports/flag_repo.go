package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
)

// FlagRepository persists and retrieves FeatureFlag records.
type FlagRepository interface {
	Create(ctx context.Context, flag *domain.FeatureFlag) error
	GetByKey(ctx context.Context, key string) (*domain.FeatureFlag, error)
	// Update atomically sets enabled, rollout_percentage, description, and
	// increments version by 1 if expectedVersion matches the stored version (CAS guard).
	// Returns the updated flag. Returns ErrFlagNotFound if key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Update(ctx context.Context, key string, expectedVersion int, enabled bool, rolloutPercentage int, description string) (*domain.FeatureFlag, error)
	List(ctx context.Context, params query.ListParams) ([]*domain.FeatureFlag, error)
	// Delete removes a feature flag by key if expectedVersion matches the stored version
	// (CAS guard). Returns the deleted entity via DELETE...RETURNING.
	// Returns ErrFlagNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Delete(ctx context.Context, key string, expectedVersion int) (*domain.FeatureFlag, error)
	// Toggle sets the enabled state of a feature flag atomically, incrementing
	// version by 1 if expectedVersion matches the stored version (CAS guard).
	// It does not overwrite rollout_percentage or description.
	// Returns the updated flag. Returns ErrFlagNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Toggle(ctx context.Context, key string, expectedVersion int, enabled bool) (*domain.FeatureFlag, error)
}
