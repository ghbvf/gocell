// Package auditquery implements the audit-query slice: query audit entries
// via HTTP using ledger.Store.
package auditquery

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// auditSort defines the default sort for audit listings: newest first.
var auditSort = []query.SortColumn{
	{Name: "timestamp", Direction: query.SortDESC},
	{Name: "id", Direction: query.SortASC},
}

// Service implements audit query business logic using ledger.Store.
type Service struct {
	store   ledger.Store
	codec   *query.CursorCodec
	logger  *slog.Logger
	runMode query.RunMode
}

// NewService creates an audit-query Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// codec must be non-nil — pagination cannot be served without a cursor codec.
func NewService(store ledger.Store, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	if codec == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
			"auditquery: cursor codec is required")
	}
	return &Service{store: store, codec: codec, logger: logger, runMode: runMode}, nil
}

// Query returns a paginated page of audit entries matching the given filters.
func (s *Service) Query(
	ctx context.Context, filters ledger.AuditFilters, pageReq query.PageParams,
) (query.PageResult[*ledger.Entry], error) {
	qctx := query.QueryContext("endpoint", "audit-query",
		"eventType", filters.EventType,
		"actorId", filters.ActorID,
		"from", filters.From.Format(time.RFC3339Nano),
		"to", filters.To.Format(time.RFC3339Nano),
	)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*ledger.Entry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       auditSort,
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*ledger.Entry, error) {
			// Fetch all matching entries (no limit), then apply in-memory cursor and sort.
			// ledger.Store.Query supports only simple offset; keyset semantics
			// are provided here at the service layer for MemStore compatibility.
			// The PG store in S8+ will push cursor logic into SQL.
			all, err := s.store.Query(ctx, filters, ledger.QueryListParams{})
			if err != nil {
				return nil, fmt.Errorf("audit-query: query: %w", err)
			}
			query.Sort(all, params.Sort, compareEntryField)
			return query.ApplyCursor(all, params, entryFieldValue)
		},
		Extract: func(e *ledger.Entry) []any {
			return []any{e.Timestamp.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "auditquery"),
		RunMode:     s.runMode,
	})
}

// compareEntryField compares a single named field of two ledger entries.
func compareEntryField(a, b *ledger.Entry, field string) int {
	switch field {
	case "timestamp":
		return a.Timestamp.Compare(b.Timestamp)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	default:
		return 0
	}
}

// entryFieldValue extracts a cursor-comparable value from a ledger entry by field name.
func entryFieldValue(e *ledger.Entry, field string) any {
	switch field {
	case "timestamp":
		return e.Timestamp
	case "id":
		return e.ID
	default:
		return ""
	}
}
