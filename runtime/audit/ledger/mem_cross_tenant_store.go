package ledger

import (
	"context"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Compile-time assertion: MemCrossTenantStore implements the interface.
var _ CrossTenantQueryStore = (*MemCrossTenantStore)(nil)

// MemCrossTenantStore is an in-memory CrossTenantQueryStore for dev topology and
// unit testing. It enumerates ALL tenants across ALL supplied MemStores, applies
// AuditFilters, merge-sorts by QuerySort, and keyset-paginates — mirroring what
// MultiStore + a production admin pool would do, but without any DB.
//
// Like MultiStore, MemCrossTenantStore is read-only: it does not implement
// ledger.Store (no Append / Tail / GetBySeq / Verify / RepoReady) so a
// misconfiguration that injects it as a write store fails at compile time.
//
// Each MemStore supplied to NewMemCrossTenantStore is one namespace shard (relay
// or bootstrap). The auditcore cell wires two mem stores; this aggregator spans
// both. Typical usage in a demo/memory topology:
//
//	relay, _ := ledger.NewMemStore(relayProto, clk)
//	bootstrap, _ := ledger.NewMemStore(bootstrapProto, clk)
//	ct, _ := ledger.NewMemCrossTenantStore(relay, bootstrap)
//
// ref: MultiStore — same fan-out + merge-sort pattern for per-namespace fan-out
// within a single tenant read.
type MemCrossTenantStore struct {
	stores []*MemStore
}

// NewMemCrossTenantStore wraps one or more MemStores into a cross-tenant
// read-only aggregator. Returns an error when:
//   - fewer than one store is supplied (a zero-store aggregator is meaningless)
//   - any store is nil
func NewMemCrossTenantStore(stores ...*MemStore) (*MemCrossTenantStore, error) {
	if len(stores) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: NewMemCrossTenantStore requires at least one backing store")
	}
	for i, s := range stores {
		if validation.IsNilInterface(s) || s == nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: NewMemCrossTenantStore received nil store",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("stores[%d] nil", i))))
		}
	}
	cp := make([]*MemStore, len(stores))
	copy(cp, stores)
	return &MemCrossTenantStore{stores: cp}, nil
}

// QueryCrossTenant enumerates ALL tenants across ALL backing MemStores, applies
// AuditFilters, merge-sorts by params.Sort (callers pass QuerySort —
// timestamp DESC, id ASC), and keyset-paginates via query.ApplyCursor.
//
// It returns up to params.FetchLimit() entries for N+1 hasMore detection.
// Returns an empty (non-nil) slice when no entries match.
//
// params.Sort must be non-empty (callers pass QuerySort); an empty Sort returns
// ErrValidationFailed, exactly as MemStore.Query and MultiStore.Query do.
//
// The ctv parameter enforces the typed-funnel (#1760); the RowScopeAll owner
// dimension is unrestricted, so no actor_id filtering is applied.
func (m *MemCrossTenantStore) QueryCrossTenant(
	_ context.Context,
	_ tenant.CrossTenantVisibility,
	filters AuditFilters,
	params query.ListParams,
) ([]*Entry, error) {
	if len(params.Sort) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: cross-tenant query requires a non-empty sort")
	}

	candidates := m.collectAllEntries(filters)

	sort.SliceStable(candidates, func(i, j int) bool {
		return compareBySort(candidates[i], candidates[j], params.Sort) < 0
	})

	results, err := query.ApplyCursor(candidates, params, entryFieldValue)
	if err != nil {
		return nil, err
	}
	if results == nil {
		results = []*Entry{}
	}
	return results, nil
}

// collectAllEntries reads every entry from every chain of every MemStore and
// returns those matching AuditFilters. Each MemStore is lock-protected via
// its own mutex; we acquire and release the lock per store to avoid nested locking.
//
// RowScopeAll allows every actor_id, so vis.Allows is always true — no owner
// predicate is applied. The TENANT boundary is crossed by construction: the
// entire cross-tenant read is the intended capability.
func (m *MemCrossTenantStore) collectAllEntries(filters AuditFilters) []*Entry {
	var out []*Entry
	for _, store := range m.stores {
		out = append(out, collectFromMemStore(store, filters)...)
	}
	return out
}

// collectFromMemStore reads all matching entries from a single MemStore under
// its lock. Extracted to keep collectAllEntries's cognitive complexity ≤ 15.
func collectFromMemStore(store *MemStore, filters AuditFilters) []*Entry {
	store.mu.Lock()
	defer store.mu.Unlock()

	var out []*Entry
	for _, chain := range store.chains {
		for _, e := range chain.entries {
			if matchesFilters(e, filters) {
				out = append(out, copyEntry(e))
			}
		}
	}
	return out
}
