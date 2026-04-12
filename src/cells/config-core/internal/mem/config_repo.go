// Package mem provides in-memory repository implementations for config-core.
// These are Phase 2 stubs for development and testing.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

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

	// Sort by params.Sort columns.
	slices.SortFunc(all, func(a, b *domain.ConfigEntry) int {
		for _, col := range params.Sort {
			v := compareConfigField(a, b, col.Name)
			if col.Direction == query.SortDESC {
				v = -v
			}
			if v != 0 {
				return v
			}
		}
		return 0
	})

	// Apply cursor filter: skip rows until we pass the cursor position.
	start := 0
	if params.CursorValues != nil {
		for i, e := range all {
			if configAfterCursor(e, params.Sort, params.CursorValues) {
				start = i
				break
			}
			if i == len(all)-1 {
				start = len(all) // cursor past all rows
			}
		}
	}

	end := start + params.FetchLimit()
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], nil
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

// configAfterCursor returns true if the entry is strictly after the cursor
// position according to the sort columns and their directions.
func configAfterCursor(e *domain.ConfigEntry, cols []query.SortColumn, cursorValues []any) bool {
	for level := 0; level < len(cols); level++ {
		val := configFieldValue(e, cols[level].Name)
		curVal := cursorValues[level]
		c := configCompareAny(val, curVal)

		if level < len(cols)-1 {
			if c != 0 {
				if cols[level].Direction == query.SortDESC {
					return c < 0
				}
				return c > 0
			}
			continue
		}
		// Last column: strict inequality.
		if cols[level].Direction == query.SortDESC {
			return c < 0
		}
		return c > 0
	}
	return false
}

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
		return e.CreatedAt.Format(time.RFC3339Nano)
	case "updated_at":
		return e.UpdatedAt.Format(time.RFC3339Nano)
	default:
		return ""
	}
}

// configCompareAny compares two values that are either string or float64.
func configCompareAny(a, b any) int {
	aStr, aOk := a.(string)
	bStr, bOk := b.(string)
	if aOk && bOk {
		return cmp.Compare(aStr, bStr)
	}
	aFloat, aOk := a.(float64)
	bFloat, bOk := b.(float64)
	if aOk && bOk {
		return cmp.Compare(aFloat, bFloat)
	}
	panic(fmt.Sprintf("configCompareAny: unsupported type combination %T vs %T", a, b))
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
