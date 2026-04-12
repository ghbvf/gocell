package mem

import (
	"cmp"
	"context"
	"slices"
	"strings"
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

	// Sort by params.Sort columns.
	slices.SortFunc(all, func(a, b *domain.FeatureFlag) int {
		for _, col := range params.Sort {
			v := compareFlagField(a, b, col.Name)
			if strings.ToUpper(col.Direction) == "DESC" {
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
		for i, f := range all {
			if flagAfterCursor(f, params.Sort, params.CursorValues) {
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

// flagAfterCursor returns true if the flag is strictly after the cursor
// position according to the sort columns and their directions.
func flagAfterCursor(f *domain.FeatureFlag, cols []query.SortColumn, cursorValues []any) bool {
	for level := 0; level < len(cols); level++ {
		val := flagFieldValue(f, cols[level].Name)
		curVal := cursorValues[level]
		c := compareFlagAny(val, curVal)

		if level < len(cols)-1 {
			if c != 0 {
				if strings.ToUpper(cols[level].Direction) == "DESC" {
					return c < 0
				}
				return c > 0
			}
			continue
		}
		// Last column: strict inequality.
		if strings.ToUpper(cols[level].Direction) == "DESC" {
			return c < 0
		}
		return c > 0
	}
	return false
}

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

// compareFlagAny compares two values that are either string or float64.
func compareFlagAny(a, b any) int {
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
	return 0
}
