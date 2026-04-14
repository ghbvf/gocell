package mem

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
)

var _ ports.ArchiveStore = (*ArchiveStore)(nil)

// ArchiveStore is an in-memory implementation of ports.ArchiveStore.
type ArchiveStore struct {
	mu      sync.Mutex
	entries []*domain.AuditEntry
}

// NewArchiveStore creates an empty in-memory ArchiveStore.
func NewArchiveStore() *ArchiveStore {
	return &ArchiveStore{
		entries: make([]*domain.AuditEntry, 0),
	}
}

func (s *ArchiveStore) Archive(_ context.Context, entries []*domain.AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, e := range entries {
		clone := *e
		s.entries = append(s.entries, &clone)
	}
	return nil
}

// Len returns the number of archived entries (for testing).
func (s *ArchiveStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
