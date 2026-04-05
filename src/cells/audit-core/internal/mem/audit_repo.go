// Package mem provides in-memory repository implementations for audit-core.
package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
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

func (r *AuditRepository) Query(_ context.Context, filters ports.AuditFilters) ([]*domain.AuditEntry, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*domain.AuditEntry
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
		result = append(result, &clone)
	}
	return result, nil
}

// Len returns the number of entries (for testing).
func (r *AuditRepository) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries)
}
