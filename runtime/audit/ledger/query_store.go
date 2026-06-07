package ledger

import (
	"context"

	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
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
	// Query has identical semantics to Store.Query — see that method's godoc for
	// the full contract (keyset pagination + params.Sort requirement, and the vis
	// row-visibility obligation enforced on the actor_id owner column; RowScopeAll
	// applies no owner predicate, stores are pure PEPs). This narrow read-only
	// subset exists so read-side aggregators (MultiStore) implement only Query,
	// not the write path.
	Query(ctx context.Context, vis tenant.RowVisibility, filters AuditFilters, params query.ListParams) ([]*Entry, error)
}
