// Package auditquery implements the audit-query slice: query audit entries
// via HTTP using ledger.Store.
package auditquery

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
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
	store ledger.QueryStore  `gocell:"required"`
	codec *query.CursorCodec `gocell:"required" gocellCode:"ErrCellMissingCodec" gocellErr:"auditquery: cursor codec is required"`
	// txRunner wraps each page fetch in a tenant-scoped RunInTx so the FORCE RLS
	// app.tenant_id GUC (set from the post-auth ctxkeys.TenantID via
	// tenantScopeForTx) is active on the read connection (#1618). No
	// tenant.WithScope is used — the post-auth principal tenant flows through the
	// ctxkeys fallback. In demo/mem mode RunInTx is a passthrough.
	txRunner persistence.CellTxManager `gocell:"required" gocellErr:"auditquery: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger   *slog.Logger
	runMode  query.RunMode
}

// NewService creates an audit-query Service. runMode controls cursor
// fail-open vs fail-closed semantics; pass query.RunModeProd unless the
// assembly declares DurabilityDemo.
//
// store, codec and txRunner must be non-nil; codec is required for pagination,
// txRunner for the tenant-scoped RunInTx that activates FORCE RLS on reads.
func NewService(
	store ledger.QueryStore, codec *query.CursorCodec, logger *slog.Logger,
	txRunner persistence.CellTxManager, runMode query.RunMode,
) (*Service, error) {
	s := &Service{store: store, codec: codec, txRunner: txRunner, logger: logger, runMode: runMode}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Query returns a paginated page of audit entries within tenant t matching the
// given filters. t is the tenant axis (#1618), passed from the authenticated
// principal by the handler; vis is the row-visibility obligation enforced on the
// actor_id owner column by the underlying store. Each page fetch runs inside a
// tenant-scoped RunInTx so DB-layer FORCE RLS is active.
func (s *Service) Query(
	ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, filters ledger.AuditFilters, pageReq query.PageParams,
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
	if filters.TraceID != "" {
		// traceId participates in the cursor scope fingerprint exactly like
		// subjectId/actorId: a cursor minted for one traceId must not silently
		// resume against a different traceId or an unfiltered query (#1048).
		attrs = append(attrs, "traceId", filters.TraceID)
	}
	if t.String() != "" {
		// tenant is the primary isolation axis (#1618): a cursor minted under
		// tenant A must not silently resume under tenant B. Including it in the
		// scope fingerprint makes cross-tenant cursor replay produce a
		// "query context mismatch" error rather than silently paging the wrong
		// tenant's rows (#1337 PR-2a review U4). Sourced from the typed t param.
		attrs = append(attrs, "tenantId", t.String())
	}
	// The row-visibility obligation is a cursor-scope axis exactly like tenantId
	// (#1337 PR-4): it decides which actor's rows are paged (via the actor_id
	// owner predicate). A cursor minted under one obligation must not silently
	// resume under another — e.g. if the caller's role changes mid-pagination
	// (self → tenant or vice versa), cursor replay must produce a "query context
	// mismatch" rather than page the wrong owner set. Subject only varies the
	// scope for self/device (empty for tenant), so both axes participate.
	attrs = append(attrs, "rowScope", vis.Scope().String())
	if vis.Subject() != "" {
		attrs = append(attrs, "rowSubject", vis.Subject())
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
			//
			// The store read runs inside RunInTx so the FORCE RLS app.tenant_id GUC
			// is set from the post-auth ctxkeys.TenantID (#1618); MultiStore fans
			// the read out to every backing PG store on the same ambient tx, so one
			// RunInTx scopes them all. Demo/mem RunInTx is a passthrough.
			var entries []*ledger.Entry
			err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
				page, qerr := s.store.Query(txCtx, t, vis, filters, params)
				if qerr != nil {
					return qerr
				}
				entries = page
				return nil
			})
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
