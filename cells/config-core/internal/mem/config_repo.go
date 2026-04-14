// Package mem provides in-memory repository implementations for config-core.
// These are Phase 2 stubs for development and testing.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"sync"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)


// Compile-time check.
var _ ports.ConfigRepository = (*ConfigRepository)(nil)

// ConfigRepository is an in-memory implementation of ports.ConfigRepository.
type ConfigRepository struct {
	mu       sync.RWMutex
	entries  map[string]*domain.ConfigEntry   // key -> entry
	versions map[string][]*domain.ConfigVersion // configID -> versions
}

// NewConfigRepository creates an empty in-memory ConfigRepository.
func NewConfigRepository() *ConfigRepository {
	return &ConfigRepository{
		entries:  make(map[string]*domain.ConfigEntry),
		versions: make(map[string][]*domain.ConfigVersion),
	}
}

func (r *ConfigRepository) Create(_ context.Context, entry *domain.ConfigEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[entry.Key]; exists {
		return errcode.New(errcode.ErrConfigDuplicate, "config key already exists: "+entry.Key)
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
		return nil, errcode.New(errcode.ErrConfigNotFound, "config not found: "+key)
	}
	clone := *entry
	return &clone, nil
}

func (r *ConfigRepository) Update(_ context.Context, entry *domain.ConfigEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[entry.Key]; !exists {
		return errcode.New(errcode.ErrConfigNotFound, "config not found: "+entry.Key)
	}
	clone := *entry
	r.entries[entry.Key] = &clone
	return nil
}

func (r *ConfigRepository) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[key]; !exists {
		return errcode.New(errcode.ErrConfigNotFound, "config not found: "+key)
	}
	delete(r.entries, key)
	return nil
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
	return nil, errcode.New(errcode.ErrConfigNotFound, "version not found")
}
