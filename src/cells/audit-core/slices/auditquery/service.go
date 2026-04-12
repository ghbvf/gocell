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
	{Name: "timestamp", Direction: "DESC"},
	{Name: "id", Direction: "ASC"},
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

	var cursorValues []any
	if pageReq.Cursor != "" {
		cur, err := s.codec.Decode(pageReq.Cursor)
		if err != nil {
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

	return s.buildResult(entries, pageReq.Limit)
}

func (s *Service) buildResult(items []*domain.AuditEntry, limit int) (query.PageResult[*domain.AuditEntry], error) {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}

	var result query.PageResult[*domain.AuditEntry]
	result.Items = items
	result.HasMore = hasMore

	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := query.Cursor{Values: []any{
			last.Timestamp.Format(time.RFC3339Nano),
			last.ID,
		}}
		token, err := s.codec.Encode(cur)
		if err != nil {
			return query.PageResult[*domain.AuditEntry]{}, err
		}
		result.NextCursor = token
	}

	if result.Items == nil {
		result.Items = []*domain.AuditEntry{}
	}

	return result, nil
}
