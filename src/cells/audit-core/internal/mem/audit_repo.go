// Package mem provides in-memory repository implementations for audit-core.
package mem

import (
	"cmp"
	"context"
	"slices"
	"sync"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/query"
)

var _ ports.AuditRepository = (*AuditRepository)(nil)

// AuditRepository is an in-memory implementation of ports.AuditRepository.
type AuditRepository struct {
	mu      sync.RWMutex
	entries []*domain.AuditEntry
}

// NewAuditRepository creates an empty in-memory AuditRepository.
func NewAuditRepository() *AuditRepository {
	return &AuditRepository{
		entries: make([]*domain.AuditEntry, 0),
	}
}

func (r *AuditRepository) Append(_ context.Context, entry *domain.AuditEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	clone := *entry
	r.entries = append(r.entries, &clone)
	return nil
}

func (r *AuditRepository) GetRange(_ context.Context, from, to int) ([]*domain.AuditEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if from < 0 {
		from = 0
	}
	if to > len(r.entries) {
		to = len(r.entries)
	}
	if from >= to {
		return nil, nil
	}

	result := make([]*domain.AuditEntry, to-from)
	for i, e := range r.entries[from:to] {
		clone := *e
		result[i] = &clone
	}
	return result, nil
}

func (r *AuditRepository) Query(_ context.Context, filters ports.AuditFilters, params query.ListParams) ([]*domain.AuditEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 1. Filter
	var filtered []*domain.AuditEntry
	for _, e := range r.entries {
		if filters.EventType != "" && e.EventType != filters.EventType {
			continue
		}
		if filters.ActorID != "" && e.ActorID != filters.ActorID {
			continue
		}
		if !filters.From.IsZero() && e.Timestamp.Before(filters.From) {
			continue
		}
		if !filters.To.IsZero() && e.Timestamp.After(filters.To) {
			continue
		}
		clone := *e
		filtered = append(filtered, &clone)
	}

	// 2. Sort
	if len(params.Sort) > 0 {
		slices.SortFunc(filtered, func(a, b *domain.AuditEntry) int {
			for _, col := range params.Sort {
				var c int
				switch col.Name {
				case "timestamp":
					c = a.Timestamp.Compare(b.Timestamp)
				case "id":
					c = cmp.Compare(a.ID, b.ID)
				}
				if col.Direction == query.SortDESC {
					c = -c
				}
				if c != 0 {
					return c
				}
			}
			return 0
		})
	}

	// 3. Apply cursor (skip entries at or before the cursor position)
	if len(params.CursorValues) >= 2 {
		cursorTS, _ := params.CursorValues[0].(string)
		cursorID, _ := params.CursorValues[1].(string)
		var after []*domain.AuditEntry
		for _, e := range filtered {
			ts := e.Timestamp.Format("2006-01-02T15:04:05.999999999Z07:00")
			// Sort is timestamp DESC, id ASC: skip while (ts > cursorTS) or (ts == cursorTS && id <= cursorID)
			if ts > cursorTS {
				continue
			}
			if ts == cursorTS && e.ID <= cursorID {
				continue
			}
			after = append(after, e)
		}
		filtered = after
	}

	// 4. Limit
	limit := params.FetchLimit()
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}

	return filtered, nil
}

// Len returns the number of entries (for testing).
func (r *AuditRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
