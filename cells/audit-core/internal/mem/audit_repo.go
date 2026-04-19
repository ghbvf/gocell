// Package mem provides in-memory repository implementations for audit-core.
package mem

import (
	"cmp"
	"context"
	"fmt"
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
		return []*domain.AuditEntry{}, nil
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

	filtered := filterEntries(r.entries, filters)
	query.Sort(filtered, params.Sort, compareAuditField)
	result, err := query.ApplyCursor(filtered, params, auditFieldValue)
	if err != nil {
		return nil, fmt.Errorf("audit-repo: query: %w", err)
	}
	return result, nil
}

// filterEntries returns clones of entries matching the given filters.
func filterEntries(entries []*domain.AuditEntry, filters ports.AuditFilters) []*domain.AuditEntry {
	var out []*domain.AuditEntry
	for _, e := range entries {
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
		out = append(out, &clone)
	}
	return out
}

// compareAuditField compares a single field of two audit entries.
func compareAuditField(a, b *domain.AuditEntry, field string) int {
	switch field {
	case "timestamp":
		return a.Timestamp.Compare(b.Timestamp)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// auditFieldValue extracts a cursor-comparable value from an audit entry.
func auditFieldValue(e *domain.AuditEntry, field string) any {
	switch field {
	case "timestamp":
		return e.Timestamp
	case "id":
		return e.ID
	default:
		return ""
	}
}

// Len returns the number of entries (for testing).
func (r *AuditRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
