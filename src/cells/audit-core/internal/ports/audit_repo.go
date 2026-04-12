// Package ports defines the driven-side interfaces for audit-core.
package ports

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/pkg/query"
)

// AuditFilters constrains audit log queries.
type AuditFilters struct {
	EventType string
	ActorID   string
	From      time.Time
	To        time.Time
}

// AuditRepository persists and queries AuditEntry records.
type AuditRepository interface {
	Append(ctx context.Context, entry *domain.AuditEntry) error
	GetRange(ctx context.Context, from, to int) ([]*domain.AuditEntry, error)
	Query(ctx context.Context, filters AuditFilters, params query.ListParams) ([]*domain.AuditEntry, error)
}
