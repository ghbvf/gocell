package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// FlagRepository persists and retrieves FeatureFlag records.
//
// Tenant scoping (epic #1337 PR-2b): every method takes tenant.TenantID as a
// mandatory typed positional parameter (param[1], right after ctx); "漏传" is a
// compile error. There is no by-PK carve-out — flags are keyed by (tenant, key).
// Implementations apply a strict `WHERE tenant_id = $N` equality predicate;
// cross-tenant rows return not-found.
type FlagRepository interface {
	Create(ctx context.Context, t tenant.TenantID, flag *domain.FeatureFlag) error
	GetByKey(ctx context.Context, t tenant.TenantID, key string) (*domain.FeatureFlag, error)
	// Update atomically sets enabled, rollout_percentage, description, and
	// increments version by 1 if expectedVersion matches the stored version (CAS guard).
	// Returns the updated flag. Returns ErrFlagNotFound if key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Update(ctx context.Context, t tenant.TenantID, key string, expectedVersion int,
		enabled bool, rolloutPercentage int, description string) (*domain.FeatureFlag, error)
	List(ctx context.Context, t tenant.TenantID, params query.ListParams) ([]*domain.FeatureFlag, error)
	// Delete removes a feature flag by key if expectedVersion matches the stored version
	// (CAS guard). Returns the deleted entity via DELETE...RETURNING.
	// Returns ErrFlagNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Delete(ctx context.Context, t tenant.TenantID, key string, expectedVersion int) (*domain.FeatureFlag, error)
	// Toggle sets the enabled state of a feature flag atomically, incrementing
	// version by 1 if expectedVersion matches the stored version (CAS guard).
	// It does not overwrite rollout_percentage or description.
	// Returns the updated flag. Returns ErrFlagNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Toggle(ctx context.Context, t tenant.TenantID, key string, expectedVersion int, enabled bool) (*domain.FeatureFlag, error)
}
