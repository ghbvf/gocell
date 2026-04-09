package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
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

func (r *FlagRepository) List(_ context.Context) ([]*domain.FeatureFlag, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*domain.FeatureFlag, 0, len(r.flags))
	for _, f := range r.flags {
		clone := *f
		result = append(result, &clone)
	}
	return result, nil
}
