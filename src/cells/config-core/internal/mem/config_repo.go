// Package mem provides in-memory repository implementations for config-core.
// These are Phase 2 stubs for development and testing.
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
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

func (r *ConfigRepository) List(_ context.Context) ([]*domain.ConfigEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*domain.ConfigEntry, 0, len(r.entries))
	for _, e := range r.entries {
		clone := *e
		result = append(result, &clone)
	}
	return result, nil
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
