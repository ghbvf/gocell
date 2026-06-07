// Package ports defines the driven-side interfaces for configcore.
package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// ConfigRepository persists and retrieves ConfigEntry and ConfigVersion records.
// It also implements healthz.RepoProber via RepoReady — a differentiated
// readiness check that exercises the cell's own relations (config_entries,
// feature_flags, and config_versions) rather than a bare connection ping,
// surfacing schema/migration drift that the pool-level postgres_ready probe
// cannot detect. See kernel/healthz.RepoProber for the full contract.
//
// Tenant scoping (epic #1337 PR-2b): every data method takes tenant.TenantID as
// a mandatory typed positional parameter (param[1], right after ctx); "漏传" is a
// compile error (the type-system Hard guarantee). There is NO by-PK carve-out —
// GetByKey/GetVersion are tenant+key / tenant+configID scoped. The internal
// control-plane path (configreadinternal) derives the tenant from the
// X-Tenant-ID request header and passes the real per-tenant UUID. Implementations
// apply a strict `WHERE tenant_id = $N` equality predicate; cross-tenant rows
// return not-found. RepoReady is the ONLY tenant-less method: it is a
// schema-existence probe, not a data read, so a tenant predicate is meaningless
// (carve-out in archtest TENANT-REPO-PARAM-FUNNEL-01).
type ConfigRepository interface {
	Create(ctx context.Context, t tenant.TenantID, entry *domain.ConfigEntry) error
	GetByKey(ctx context.Context, t tenant.TenantID, key string) (*domain.ConfigEntry, error)
	// Update atomically sets value and increments version if expectedVersion matches
	// the stored version (CAS guard). Preserves the existing sensitive flag — the
	// repo reads it internally via SELECT...FOR UPDATE to eliminate any TOCTOU race
	// on the sensitive flag. Returns ErrConfigRepoNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Update(ctx context.Context, t tenant.TenantID, key string, expectedVersion int, value string) (*domain.ConfigEntry, error)
	// UpdateForRollback atomically sets value AND sensitive, increments version,
	// provided expectedVersion matches the stored version (CAS guard).
	// Used exclusively by configpublish.Rollback to restore a snapshot's sensitivity
	// alongside its value. Returns ErrConfigRepoNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	UpdateForRollback(ctx context.Context, t tenant.TenantID, key string,
		expectedVersion int, value string, sensitive bool) (*domain.ConfigEntry, error)
	// Delete removes a config entry by key if expectedVersion matches the stored
	// version (CAS guard). Returns ErrConfigRepoNotFound if the key does not exist,
	// or ErrVersionConflict if expectedVersion does not match.
	Delete(ctx context.Context, t tenant.TenantID, key string, expectedVersion int) (*domain.ConfigEntry, error)
	List(ctx context.Context, t tenant.TenantID, params query.ListParams) ([]*domain.ConfigEntry, error)
	PublishVersion(ctx context.Context, t tenant.TenantID, version *domain.ConfigVersion) error
	GetVersion(ctx context.Context, t tenant.TenantID, configID string, version int) (*domain.ConfigVersion, error)
	// RepoReady implements healthz.RepoProber. It issues three cheap
	// non-transactional representative queries — one against config_entries,
	// one against feature_flags, and one against config_versions — so that
	// missing tables or permission loss are detected independently of the
	// pool-level postgres_ready probe. config_versions is included because it
	// is load-bearing for tenant-scoped versioning (PR-2b #1479). In-memory
	// implementations return nil (always ready). This is a schema-existence
	// probe, not a tenant-scoped data read, so it takes no tenant parameter.
	RepoReady(ctx context.Context) error
}
