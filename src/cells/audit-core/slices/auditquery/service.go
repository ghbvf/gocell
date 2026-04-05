// Package auditquery implements the audit-query slice: query audit entries
// via HTTP.
package auditquery

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
)

// Service implements audit query business logic.
type Service struct {
	repo   ports.AuditRepository
	logger *slog.Logger
}

// NewService creates an audit-query Service.
func NewService(repo ports.AuditRepository, logger *slog.Logger) *Service {
	return &Service{repo: repo, logger: logger}
}

// Query returns audit entries matching the given filters.
func (s *Service) Query(ctx context.Context, filters ports.AuditFilters) ([]*domain.AuditEntry, error) {
	entries, err := s.repo.Query(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("audit-query: query: %w", err)
	}
	return entries, nil
}
