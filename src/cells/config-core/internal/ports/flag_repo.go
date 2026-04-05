package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
)

// FlagRepository persists and retrieves FeatureFlag records.
type FlagRepository interface {
	Create(ctx context.Context, flag *domain.FeatureFlag) error
	GetByKey(ctx context.Context, key string) (*domain.FeatureFlag, error)
	Update(ctx context.Context, flag *domain.FeatureFlag) error
	List(ctx context.Context) ([]*domain.FeatureFlag, error)
}
