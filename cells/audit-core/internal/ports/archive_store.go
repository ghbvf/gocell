package ports

import (
	"context"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
)

// ArchiveStore moves audit entries to long-term cold storage.
type ArchiveStore interface {
	Archive(ctx context.Context, entries []*domain.AuditEntry) error
}
