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
	repo    ports.AuditRepository
	codec   *query.CursorCodec
	logger  *slog.Logger
	runMode query.RunMode
}

// NewService creates an audit-query Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination cannot be served without a cursor codec.
// Passing nil is a caller programming error and fails fast at construction.
func NewService(repo ports.AuditRepository, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) *Service {
	if codec == nil {
		panic("auditquery: cursor codec is required")
	}
	return &Service{repo: repo, codec: codec, logger: logger, runMode: runMode}
}

// Query returns a paginated page of audit entries matching the given filters.
func (s *Service) Query(ctx context.Context, filters ports.AuditFilters, pageReq query.PageRequest) (query.PageResult[*domain.AuditEntry], error) {
	qctx := query.QueryContext("endpoint", "audit-query",
		"eventType", filters.EventType,
		"actorId", filters.ActorID,
		"from", filters.From.Format(time.RFC3339Nano),
		"to", filters.To.Format(time.RFC3339Nano),
	)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*domain.AuditEntry]{
		Codec:    s.codec,
		Request:  pageReq,
		Sort:     auditSort,
		QueryCtx: qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*domain.AuditEntry, error) {
			entries, err := s.repo.Query(ctx, filters, params)
			if err != nil {
				return nil, fmt.Errorf("audit-query: query: %w", err)
			}
			return entries, nil
		},
		Extract: func(e *domain.AuditEntry) []any {
			return []any{e.Timestamp.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "auditquery"),
		RunMode:     s.runMode,
	})
}
