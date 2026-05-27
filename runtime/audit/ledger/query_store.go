package ledger

import (
	"context"

	"github.com/ghbvf/gocell/pkg/query"
)

// QueryStore is the narrow read-only subset of Store used by the auditquery
// HTTP slice. Splitting Query out lets read-side aggregators (MultiStore)
// implement only this interface, so a misconfiguration that injects an
// aggregator as a write store fails at compile time rather than at the first
// Append call.
//
// Every Store automatically satisfies QueryStore by structural method-set
// inclusion (Query has the same signature); existing callers continue to wire
// concrete stores through ledger.Store without change.
//
// ref: k8s.io/apiserver/pkg/audit/union.go — read-side fan-out aggregator
// pattern (Union(backends ...Backend) Backend); the write side stays per-
// backend independent.
type QueryStore interface {
	// Query lists entries matching AuditFilters using keyset cursor pagination
	// defined by params (Limit + decoded CursorValues + Sort). It returns up to
	// params.FetchLimit() (Limit+1) rows for N+1 hasMore detection, ordered by
	// params.Sort. params.Sort must be non-empty (callers pass QuerySort);
	// an empty Sort is a programmer error and yields ErrValidationFailed.
	// Returns an empty (non-nil) slice when no entries match.
	Query(ctx context.Context, filters AuditFilters, params query.ListParams) ([]*Entry, error)
}
