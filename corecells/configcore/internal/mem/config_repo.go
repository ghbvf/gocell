// Package mem provides in-memory repository implementations for configcore.
// These are Phase 2 stubs for development and testing.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/corecells/configcore/internal/domain"
	"github.com/ghbvf/gocell/corecells/configcore/internal/ports"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

// Compile-time check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

const (
	configInternalKeyQuotedFmt = "key=%q"
	msgConfigInvalidTenant     = "config repo: invalid tenant"
	msgConfigNotFound          = "config not found"
)

// ConfigRepository is an in-memory implementation of ports.ConfigRepository.
//
// # Tenancy (#1337 PR-2b)
//
// Every data method takes a mandatory tenant.TenantID positional parameter and
// scopes all reads/writes to the tenant's inner map. Cross-tenant rows are
// structurally unreachable — they live in a different inner map — so the
// existing not-found error path fires naturally for any mismatched access.
// RepoReady is the only tenant-less method (schema-existence probe, not a data
// read; carve-out in archtest TENANT-REPO-PARAM-FUNNEL-01).
type ConfigRepository struct {
	mu       sync.RWMutex
	entries  map[tenant.TenantID]map[string]*domain.ConfigEntry     // tenant -> key -> entry
	versions map[tenant.TenantID]map[string][]*domain.ConfigVersion // tenant -> configID -> versions
	clock    clock.Clock
}

// NewConfigRepository creates an empty in-memory ConfigRepository.
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
func NewConfigRepository(clk clock.Clock) *ConfigRepository {
	clock.MustHaveClock(clk, "mem.NewConfigRepository")
	return &ConfigRepository{
		entries:  make(map[tenant.TenantID]map[string]*domain.ConfigEntry),
		versions: make(map[tenant.TenantID]map[string][]*domain.ConfigVersion),
		clock:    clk,
	}
}

// tenantEntries lazily creates and returns the inner entries map for t.
// Caller must hold mu (write lock).
func (r *ConfigRepository) tenantEntries(t tenant.TenantID) map[string]*domain.ConfigEntry {
	m, ok := r.entries[t]
	if !ok {
		m = make(map[string]*domain.ConfigEntry)
		r.entries[t] = m
	}
	return m
}

// tenantVersions lazily creates and returns the inner versions map for t.
// Caller must hold mu (write lock).
func (r *ConfigRepository) tenantVersions(t tenant.TenantID) map[string][]*domain.ConfigVersion {
	m, ok := r.versions[t]
	if !ok {
		m = make(map[string][]*domain.ConfigVersion)
		r.versions[t] = m
	}
	return m
}

func (r *ConfigRepository) Create(_ context.Context, t tenant.TenantID, entry *domain.ConfigEntry) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	te := r.tenantEntries(t)
	if _, exists := te[entry.Key]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrConfigDuplicate, "config key already exists",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(configInternalKeyQuotedFmt, entry.Key))))
	}
	clone := *entry
	te[entry.Key] = &clone
	return nil
}

//nolint:dupl // mirrors FlagRepository.GetByKey; typed differences (ConfigEntry vs FeatureFlag, distinct codes) preclude shared helper
func (r *ConfigRepository) GetByKey(_ context.Context, t tenant.TenantID, key string) (*domain.ConfigEntry, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	te, ok := r.entries[t]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, msgConfigNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(configInternalKeyQuotedFmt, key))))
	}
	entry, ok := te[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, msgConfigNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(configInternalKeyQuotedFmt, key))))
	}
	clone := *entry
	return &clone, nil
}

//nolint:dupl // mirrors FlagRepository.Update; typed differences (ConfigEntry vs FeatureFlag, distinct fields) preclude shared helper
func (r *ConfigRepository) Update(
	_ context.Context, t tenant.TenantID, key string, expectedVersion int, value string,
) (*domain.ConfigEntry, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	te := r.tenantEntries(t)
	existing, ok := te[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, msgConfigNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(configInternalKeyQuotedFmt, key))))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "config_entry", key)
	}
	existing.Value = value
	// Preserve existing Sensitive — do NOT change it.
	existing.Version++
	existing.UpdatedAt = r.clock.Now()
	clone := *existing
	return &clone, nil
}

func (r *ConfigRepository) UpdateForRollback(
	_ context.Context, t tenant.TenantID, key string, expectedVersion int, value string, sensitive bool,
) (*domain.ConfigEntry, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	te := r.tenantEntries(t)
	existing, ok := te[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, msgConfigNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(configInternalKeyQuotedFmt, key))))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "config_entry", key)
	}
	existing.Value = value
	existing.Sensitive = sensitive
	existing.Version++
	existing.UpdatedAt = r.clock.Now()
	clone := *existing
	return &clone, nil
}

func (r *ConfigRepository) Delete(_ context.Context, t tenant.TenantID, key string, expectedVersion int) (*domain.ConfigEntry, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	te := r.tenantEntries(t)
	existing, ok := te[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, msgConfigNotFound,
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(configInternalKeyQuotedFmt, key))))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "config_entry", key)
	}
	clone := *existing
	delete(te, key)
	return &clone, nil
}

// List returns config entries sorted and paginated according to params.
// It applies keyset cursor filtering and returns up to FetchLimit() rows
// for N+1 hasMore detection.
func (r *ConfigRepository) List(_ context.Context, t tenant.TenantID, params query.ListParams) ([]*domain.ConfigEntry, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	te := r.entries[t] // nil-safe: ranging over nil map is a no-op
	all := make([]*domain.ConfigEntry, 0, len(te))
	for _, e := range te {
		clone := *e
		all = append(all, &clone)
	}

	query.Sort(all, params.Sort, compareConfigField)
	result, err := query.ApplyCursor(all, params, configFieldValue)
	if err != nil {
		return nil, fmt.Errorf("config-repo: list: %w", err)
	}
	return result, nil
}

// compareConfigField compares a single field of two config entries.
func compareConfigField(a, b *domain.ConfigEntry, field string) int {
	switch field {
	case "key":
		return cmp.Compare(a.Key, b.Key)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	case "value":
		return cmp.Compare(a.Value, b.Value)
	case "version":
		return cmp.Compare(a.Version, b.Version)
	case "created_at":
		return a.CreatedAt.Compare(b.CreatedAt)
	case "updated_at":
		return a.UpdatedAt.Compare(b.UpdatedAt)
	default:
		return 0
	}
}

// configFieldValue extracts a cursor-comparable value from a config entry.
func configFieldValue(e *domain.ConfigEntry, field string) any {
	switch field {
	case "key":
		return e.Key
	case "id":
		return e.ID
	case "value":
		return e.Value
	case "version":
		return float64(e.Version)
	case "created_at":
		return e.CreatedAt
	case "updated_at":
		return e.UpdatedAt
	default:
		return ""
	}
}

func (r *ConfigRepository) PublishVersion(_ context.Context, t tenant.TenantID, version *domain.ConfigVersion) error {
	if err := t.Validate(); err != nil {
		return errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	tv := r.tenantVersions(t)
	clone := *version
	tv[version.ConfigID] = append(tv[version.ConfigID], &clone)
	return nil
}

// RepoReady implements healthz.RepoProber.
// In-memory store is always ready (MemStore convention).
func (r *ConfigRepository) RepoReady(_ context.Context) error {
	return nil
}

func (r *ConfigRepository) GetVersion(_ context.Context, t tenant.TenantID, configID string, version int) (*domain.ConfigVersion, error) {
	if err := t.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, msgConfigInvalidTenant, err)
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	tv := r.versions[t] // nil-safe: ranging over nil map is a no-op
	for _, v := range tv[configID] {
		if v.Version == version {
			clone := *v
			return &clone, nil
		}
	}
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "version not found")
}
