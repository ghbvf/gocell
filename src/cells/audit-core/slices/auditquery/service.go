// Package auditquery implements the audit-query slice: query audit entries
// via HTTP.
package auditquery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/pkg/query"
)

// auditSort defines the default sort for audit listings: newest first.
var auditSort = []query.SortColumn{
	{Name: "timestamp", Direction: query.SortDESC},
	{Name: "id", Direction: query.SortASC},
}

// Service implements audit query business logic.
type Service struct {
	repo   ports.AuditRepository
	codec  *query.CursorCodec
	logger *slog.Logger
}

// NewService creates an audit-query Service.
func NewService(repo ports.AuditRepository, codec *query.CursorCodec, logger *slog.Logger) *Service {
	return &Service{repo: repo, codec: codec, logger: logger}
}

// Query returns a paginated page of audit entries matching the given filters.
func (s *Service) Query(ctx context.Context, filters ports.AuditFilters, pageReq query.PageRequest) (query.PageResult[*domain.AuditEntry], error) {
	pageReq.Normalize()

	qctx := query.QueryContext("endpoint", "audit-query",
		"eventType", filters.EventType,
		"actorId", filters.ActorID,
		"from", filters.From.Format(time.RFC3339),
		"to", filters.To.Format(time.RFC3339),
	)

	var cursorValues []any
	if pageReq.Cursor != "" {
		cur, err := s.codec.Decode(pageReq.Cursor)
		if err != nil {
			return query.PageResult[*domain.AuditEntry]{}, err
		}
		if err := query.ValidateCursorScope(cur, auditSort, qctx); err != nil {
			return query.PageResult[*domain.AuditEntry]{}, err
		}
		cursorValues = cur.Values
	}

	params := query.ListParams{
		Limit:        pageReq.Limit,
		CursorValues: cursorValues,
		Sort:         auditSort,
	}

	entries, err := s.repo.Query(ctx, filters, params)
	if err != nil {
		return query.PageResult[*domain.AuditEntry]{}, fmt.Errorf("audit-query: query: %w", err)
	}

	return query.BuildPageResult(entries, pageReq.Limit, s.codec, auditSort, qctx, func(e *domain.AuditEntry) []any {
		return []any{e.Timestamp.Format(time.RFC3339Nano), e.ID}
	})
}
