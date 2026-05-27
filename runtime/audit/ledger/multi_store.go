package ledger

import (
	"context"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
)

// MultiStore is a read-only QueryStore aggregator across multiple Stores.
// Each backing Store has its own NamespaceID and independent HMAC chain;
// MultiStore.Query fans out the same filters+params to every backing store
// and merges the results by params.Sort (which auditquery wires to
// QuerySort — timestamp DESC, id ASC).
//
// MultiStore deliberately implements ONLY QueryStore, not the full Store
// interface. This is the Hard upstream defense for issue #1121: misconfiguring
// auditcore.WithLedgerStore(multiStore) — which would route writes through the
// aggregator and break the per-namespace chain — fails at compile time, not
// at the first event Append.
//
// Cursor semantics are namespace-independent: every Entry carries a Timestamp
// (UTC) and an ID, and the canonical (timestamp DESC, id ASC) ordering applies
// uniformly across all chains. Passing the same cursor to every backing store
// is therefore safe — each store's keyset Query correctly filters its own
// entries past the cursor boundary, and tie-breaks by id work across stores
// because IDs are UUID strings everywhere (lexical comparison is consistent).
//
// ref: k8s.io/apiserver/pkg/audit/union.go — Union(backends ...Backend) Backend
// is the canonical multi-backend read-side fan-out pattern; the write-side
// stays per-backend independent.
type MultiStore struct {
	stores []Store
}

// NewMultiStore wraps two or more Stores into a read-only fan-out aggregator.
// Returns an error when:
//   - fewer than two stores are supplied (a single-store MultiStore is just
//     an indirection — callers should use the store directly)
//   - any store is nil or typed-nil (validation.IsNilInterface)
func NewMultiStore(stores ...Store) (*MultiStore, error) {
	if len(stores) < 2 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: NewMultiStore requires at least two backing stores")
	}
	for i, s := range stores {
		if validation.IsNilInterface(s) {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: NewMultiStore received nil store",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("stores[%d] nil", i))))
		}
	}
	return &MultiStore{stores: append([]Store(nil), stores...)}, nil
}

// Query fans out to each backing store with the same filters+params, merges
// the results by params.Sort, and trims to params.FetchLimit() so the caller's
// pagination machinery (query.ExecutePagedQuery) detects hasMore via the N+1
// sentinel.
//
// Each backing store returns up to FetchLimit() rows; MultiStore collects up
// to len(stores)*FetchLimit() rows, sorts once, and trims to FetchLimit(). For
// a 2-store deployment with Limit=20 the worst case is 42 rows merged per
// page — a constant-factor cost that does not change pagination semantics.
func (m *MultiStore) Query(ctx context.Context, filters AuditFilters, params query.ListParams) ([]*Entry, error) {
	if len(params.Sort) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: query requires a non-empty sort")
	}
	var merged []*Entry
	for _, s := range m.stores {
		page, err := s.Query(ctx, filters, params)
		if err != nil {
			return nil, fmt.Errorf("multi-store query: %w", err)
		}
		merged = append(merged, page...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return compareBySort(merged[i], merged[j], params.Sort) < 0
	})
	if limit := params.FetchLimit(); limit > 0 && len(merged) > limit {
		merged = merged[:limit]
	}
	if merged == nil {
		merged = []*Entry{}
	}
	return merged, nil
}

// compareBySort returns a negative integer when a sorts before b under cols,
// positive when after, zero when equal. compareEntryField is shared with
// MemStore so QuerySort column-name semantics stay in one place.
func compareBySort(a, b *Entry, cols []query.SortColumn) int {
	for _, col := range cols {
		c := compareEntryField(a, b, col.Name)
		if c == 0 {
			continue
		}
		if col.Direction == query.SortDESC {
			return -c
		}
		return c
	}
	return 0
}
