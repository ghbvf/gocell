package mem

import (
	"cmp"
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time check.
var _ ports.FlagRepository = (*FlagRepository)(nil)

// FlagRepository is an in-memory implementation of ports.FlagRepository.
type FlagRepository struct {
	mu    sync.RWMutex
	flags map[string]*domain.FeatureFlag // key -> flag
}

// NewFlagRepository creates an empty in-memory FlagRepository.
func NewFlagRepository() *FlagRepository {
	return &FlagRepository{
		flags: make(map[string]*domain.FeatureFlag),
	}
}

func (r *FlagRepository) Create(_ context.Context, flag *domain.FeatureFlag) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.flags[flag.Key]; exists {
		return errcode.New(errcode.ErrFlagDuplicate, "flag key already exists: "+flag.Key)
	}
	clone := *flag
	r.flags[flag.Key] = &clone
	return nil
}

func (r *FlagRepository) GetByKey(_ context.Context, key string) (*domain.FeatureFlag, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	flag, ok := r.flags[key]
	if !ok {
		return nil, errcode.New(errcode.ErrFlagNotFound, "flag not found: "+key)
	}
	clone := *flag
	return &clone, nil
}

func (r *FlagRepository) Update(_ context.Context, flag *domain.FeatureFlag) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.flags[flag.Key]; !exists {
		return errcode.New(errcode.ErrFlagNotFound, "flag not found: "+flag.Key)
	}
	clone := *flag
	r.flags[flag.Key] = &clone
	return nil
}

// List returns flags sorted and paginated according to params.
// It applies keyset cursor filtering and returns up to FetchLimit() rows
// for N+1 hasMore detection.
func (r *FlagRepository) List(_ context.Context, params query.ListParams) ([]*domain.FeatureFlag, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*domain.FeatureFlag, 0, len(r.flags))
	for _, f := range r.flags {
		clone := *f
		all = append(all, &clone)
	}

	query.Sort(all, params.Sort, compareFlagField)
	return query.ApplyCursor(all, params, flagFieldValue), nil
}

// compareFlagField compares a single field of two feature flags.
func compareFlagField(a, b *domain.FeatureFlag, field string) int {
	switch field {
	case "key":
		return cmp.Compare(a.Key, b.Key)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// flagFieldValue extracts a cursor-comparable value from a feature flag.
func flagFieldValue(f *domain.FeatureFlag, field string) any {
	switch field {
	case "key":
		return f.Key
	case "id":
		return f.ID
	default:
		return ""
	}
}
