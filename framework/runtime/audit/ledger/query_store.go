package ledger

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
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
	// the full contract (the t tenant.TenantID tenant axis, keyset pagination +
	// params.Sort requirement, the vis row-visibility obligation enforced on the
	// actor_id owner column, and the RowScopeAll fail-closed rule). This narrow
	// read-only subset exists so read-side aggregators (MultiStore) implement only
	// Query, not the write path.
	Query(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, filters AuditFilters, params query.ListParams) ([]*Entry, error)

	// GetByID has identical semantics to Store.GetByID — see that method's godoc
	// for the full contract (the t tenant.TenantID tenant axis, the vis
	// row-visibility obligation enforced on the actor_id owner column with the
	// IDOR-safe ErrAuditLedgerNotFound collapse, and the RowScopeAll fail-closed
	// rule). Declared here too so read-side aggregators (MultiStore) and the
	// auditquery Service can fetch a single entry by its opaque store id through
	// the narrow read interface, without depending on the write path.
	//
	// id is an OPAQUE backend-agnostic store handle, NOT required to be a canonical
	// UUID at this interface boundary (the PG backend assigns a uuid primary key, the
	// in-memory demo backend the EventID); backends validate id shape as they see fit
	// (see Store.GetByID for the PG uuid parse-guard). The wire boundary validates it
	// as an idutil.SafeID string.
	GetByID(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, id string) (*Entry, error)
}

// Compile-time assertion of the structural-inclusion invariant documented above:
// every Store satisfies QueryStore (Store's method set ⊇ QueryStore's). If a future
// edit adds a method to QueryStore without also adding it to Store — or drops one
// from Store — this assignment stops compiling, before any caller that wires a
// concrete ledger.Store where a QueryStore is expected silently breaks.
var _ QueryStore = Store(nil)
