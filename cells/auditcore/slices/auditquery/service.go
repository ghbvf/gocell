// Package auditquery implements the audit-query slice: query audit entries
// via HTTP using ledger.Store.
package auditquery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Service implements audit query business logic against a ledger.QueryStore.
//
// The narrow QueryStore interface (just Query, no Append/Tail/Verify/...) lets
// composition roots inject a read-side aggregator like ledger.MultiStore that
// fans out reads across multiple chains (issue #1121 / ADR 202605270230 —
// auditcore relay chain + bootstrap chain). Any concrete ledger.Store also
// satisfies QueryStore by structural typing, so single-chain deployments need
// no extra wiring.
type Service struct {
	store   ledger.QueryStore  `gocell:"required"`
	codec   *query.CursorCodec `gocell:"required" gocellCode:"ErrCellMissingCodec" gocellErr:"auditquery: cursor codec is required"`
	logger  *slog.Logger
	runMode query.RunMode
}

// NewService creates an audit-query Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// Both store and codec must be non-nil; codec is required for pagination.
func NewService(store ledger.QueryStore, codec *query.CursorCodec, logger *slog.Logger, runMode query.RunMode) (*Service, error) {
	s := &Service{store: store, codec: codec, logger: logger, runMode: runMode}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Query returns a paginated page of audit entries matching the given filters.
func (s *Service) Query(
	ctx context.Context, filters ledger.AuditFilters, pageReq query.PageParams,
) (query.PageResult[*ledger.Entry], error) {
	attrs := []string{"endpoint", "audit-query"}
	if filters.EventType != "" {
		attrs = append(attrs, "eventType", filters.EventType)
	}
	if filters.ActorID != "" {
		attrs = append(attrs, "actorId", filters.ActorID)
	}
	if filters.SubjectID != "" {
		// subjectId participates in the cursor scope fingerprint exactly like
		// actorId/eventType: it changes which rows are paged, so a cursor minted
		// for one subjectId must not silently resume against another (#1290).
		attrs = append(attrs, "subjectId", filters.SubjectID)
	}
	// From/To are time-range filter predicates, not cursor-scope identifiers.
	// Including zero-value time.Time in the scope would embed "0001-01-01T00:00:00Z"
	// and break cursor reuse when callers omit From/To (F-07). Non-zero values
	// are intentionally excluded from scope as well: time-range filters narrow
	// results within a stable data type; changing From/To does not change the
	// kind of data being paged, so they must not participate in scope fingerprinting.
	qctx := query.QueryContext(attrs...)
	return query.ExecutePagedQuery(ctx, query.PagedQueryConfig[*ledger.Entry]{
		Codec:      s.codec,
		PageParams: pageReq,
		Sort:       ledger.QuerySort(),
		QueryCtx:   qctx,
		Fetch: func(ctx context.Context, params query.ListParams) ([]*ledger.Entry, error) {
			// Keyset pagination is pushed into the store (PG: SQL keyset via
			// pgquery.AppendKeyset over idx_audit_namespace_ts_id; MemStore:
			// in-memory keyset). The store returns up to params.FetchLimit()
			// (Limit+1) rows; BuildPageResult trims to Limit and detects hasMore.
			entries, err := s.store.Query(ctx, filters, params)
			if err != nil {
				return nil, fmt.Errorf("audit-query: query: %w", err)
			}
			return entries, nil
		},
		Extract: func(e *ledger.Entry) []any {
			return []any{e.Timestamp.Format(time.RFC3339Nano), e.ID}
		},
		OnCursorErr: query.LogCursorError(s.logger, "auditquery"),
		RunMode:     s.runMode,
	})
}
