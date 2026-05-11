// Package mem provides in-memory repository implementations for configcore.
// These are Phase 2 stubs for development and testing.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// Compile-time check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

const (
	configInternalKeyQuotedFmt = "key=%q"
	msgConfigNotFound          = "config not found"
)

// ConfigRepository is an in-memory implementation of ports.ConfigRepository.
type ConfigRepository struct {
	mu       sync.RWMutex
	entries  map[string]*domain.ConfigEntry     // key -> entry
	versions map[string][]*domain.ConfigVersion // configID -> versions
	clock    clock.Clock
}

// NewConfigRepository creates an empty in-memory ConfigRepository.
// clk must be non-nil; pass clock.Real() in production and clockmock.New() in tests.
func NewConfigRepository(clk clock.Clock) *ConfigRepository {
	clock.MustHaveClock(clk, "mem.NewConfigRepository")
	return &ConfigRepository{
		entries:  make(map[string]*domain.ConfigEntry),
		versions: make(map[string][]*domain.ConfigVersion),
		clock:    clk,
	}
}

func (r *ConfigRepository) Create(_ context.Context, entry *domain.ConfigEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[entry.Key]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrConfigDuplicate, "config key already exists",
			errcode.WithInternal(fmt.Sprintf(configInternalKeyQuotedFmt, entry.Key)))
	}
	clone := *entry
	r.entries[entry.Key] = &clone
	return nil
}

func (r *ConfigRepository) GetByKey(_ context.Context, key string) (*domain.ConfigEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, msgConfigNotFound,
			errcode.WithInternal(fmt.Sprintf(configInternalKeyQuotedFmt, key)))
	}
	clone := *entry
	return &clone, nil
}

func (r *ConfigRepository) Update(_ context.Context, key string, expectedVersion int, value string) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, msgConfigNotFound,
			errcode.WithInternal(fmt.Sprintf(configInternalKeyQuotedFmt, key)))
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
	_ context.Context, key string, expectedVersion int, value string, sensitive bool,
) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, msgConfigNotFound,
			errcode.WithInternal(fmt.Sprintf(configInternalKeyQuotedFmt, key)))
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

func (r *ConfigRepository) Delete(_ context.Context, key string, expectedVersion int) (*domain.ConfigEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.entries[key]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, msgConfigNotFound,
			errcode.WithInternal(fmt.Sprintf(configInternalKeyQuotedFmt, key)))
	}
	if existing.Version != expectedVersion {
		return nil, cas.CheckVersionMatch(0, "config_entry", key)
	}
	clone := *existing
	delete(r.entries, key)
	return &clone, nil
}

// List returns config entries sorted and paginated according to params.
// It applies keyset cursor filtering and returns up to FetchLimit() rows
// for N+1 hasMore detection.
func (r *ConfigRepository) List(_ context.Context, params query.ListParams) ([]*domain.ConfigEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*domain.ConfigEntry, 0, len(r.entries))
	for _, e := range r.entries {
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

func (r *ConfigRepository) PublishVersion(_ context.Context, version *domain.ConfigVersion) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	clone := *version
	r.versions[version.ConfigID] = append(r.versions[version.ConfigID], &clone)
	return nil
}

func (r *ConfigRepository) GetVersion(_ context.Context, configID string, version int) (*domain.ConfigVersion, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	versions := r.versions[configID]
	for _, v := range versions {
		if v.Version == version {
			clone := *v
			return &clone, nil
		}
	}
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "version not found")
}
