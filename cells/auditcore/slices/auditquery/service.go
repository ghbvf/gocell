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

// auditQueryFetchCap is the maximum number of entries fetched from the store in
// a single Query call. It acts as a defensive ceiling to prevent unbounded
// memory growth when the audit_entries table grows large and callers do not
// narrow their filter predicates sufficiently.
//
// This is an application-level guard only — it does not replace real keyset
// pagination pushed into the SQL layer. The follow-up backlog item
// S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01 tracks the proper implementation: extend
// ledger.Store.Query to accept a keyset cursor (mirroring
// cells/configcore/internal/adapters/postgres/config_repo.go::List) so that
// large result sets are streamed page-by-page from PG rather than loaded into
// memory in full.
const auditQueryFetchCap = 5000

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
			// Fetch up to auditQueryFetchCap entries, then apply in-memory cursor
			// and sort. ledger.Store.Query supports only simple LIMIT; keyset
			// semantics are provided here at the service layer for MemStore
			// compatibility.
			//
			// auditQueryFetchCap (5000) is a defensive ceiling against OOM when
			// the audit_entries table grows large and filters are not narrow enough.
			// Real keyset pagination pushed into SQL is tracked in backlog item
			// S8-AUDIT-QUERY-KEYSET-PUSH-DOWN-01.
			all, err := s.store.Query(ctx, filters, ledger.QueryListParams{Limit: auditQueryFetchCap})
			if err != nil {
				return nil, fmt.Errorf("audit-query: query: %w", err)
			}
			if len(all) >= auditQueryFetchCap {
				s.logger.Warn("audit-query: fetch cap reached; results may be incomplete — narrow filters or await S8 keyset pagination",
					slog.Int("cap", auditQueryFetchCap),
					slog.String("eventType", filters.EventType),
					slog.String("actorId", filters.ActorID),
				)
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
