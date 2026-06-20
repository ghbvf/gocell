package ledger

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// Compile-time assertion: MemChainVerifyStore implements the interface.
var _ ChainVerifyStore = (*MemChainVerifyStore)(nil)

// MemChainVerifyStore is an in-memory ChainVerifyStore for dev topology and unit
// testing the #1755 verify orchestrator without a database. Each MemStore supplied
// is one namespace shard (relay or bootstrap); the store enumerates every
// (namespace, tenant) chain across all shards and reuses each shard's own Protocol
// for the HMAC recompute (so the wrong-namespace-protocol false-alarm hazard the
// PG impl guards against cannot arise — each shard verifies only its own
// namespace).
//
// Like MemCrossTenantStore it is read-only: it does not implement ledger.Store, so
// a misconfiguration that injects it as a write store fails at compile time.
//
// ref: MemCrossTenantStore — same per-namespace MemStore fan-out pattern.
type MemChainVerifyStore struct {
	stores []*MemStore
}

// NewMemChainVerifyStore wraps one or more MemStores into a chain-verify
// aggregator. Returns an error when fewer than one store is supplied or any store
// is nil.
func NewMemChainVerifyStore(stores ...*MemStore) (*MemChainVerifyStore, error) {
	if len(stores) == 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"audit ledger: NewMemChainVerifyStore requires at least one backing store")
	}
	for i, s := range stores {
		if validation.IsNilInterface(s) || s == nil {
			return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"audit ledger: NewMemChainVerifyStore received nil store",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("stores[%d] nil", i))))
		}
	}
	cp := make([]*MemStore, len(stores))
	copy(cp, stores)
	return &MemChainVerifyStore{stores: cp}, nil
}

// EnumerateChains returns one ChainRef per non-empty (namespace, tenant) chain
// across every backing MemStore. Mem chains are always gap-free starting at
// SeqNo=1, so MinSeq is 1 and MaxSeq is the chain length. Empty chains (created
// but never appended) are skipped — they have no integrity to verify.
func (m *MemChainVerifyStore) EnumerateChains(_ context.Context) ([]ChainRef, error) {
	refs := []ChainRef{}
	for _, store := range m.stores {
		refs = append(refs, enumerateMemStoreChains(store)...)
	}
	return refs, nil
}

// enumerateMemStoreChains reads the (namespace, tenant) chains of a single
// MemStore under its lock. Extracted to avoid nested locking and keep
// EnumerateChains's cognitive complexity low (mirrors collectFromMemStore).
func enumerateMemStoreChains(store *MemStore) []ChainRef {
	store.mu.Lock()
	defer store.mu.Unlock()
	ns := string(store.protocol.Namespace())
	var refs []ChainRef
	for tenantKey, chain := range store.chains {
		n := int64(len(chain.entries))
		if n == 0 {
			continue
		}
		refs = append(refs, ChainRef{Namespace: ns, TenantID: tenantKey, MinSeq: 1, MaxSeq: n})
	}
	return refs
}

// VerifyChain re-computes the HMAC chain for [fromSeq, toSeq] of the explicit
// (namespace, tenant) chain. It dispatches to the MemStore shard whose Protocol
// namespace matches; a namespace with no matching shard FAILS CLOSED (error, not
// valid=false), mirroring the PG AuditChainVerifyStore — a missing namespace is a
// misconfiguration, not tamper.
func (m *MemChainVerifyStore) VerifyChain(_ context.Context, namespace, tenantID string, fromSeq, toSeq int64) (bool, int64, error) {
	for _, store := range m.stores {
		if string(store.protocol.Namespace()) != namespace {
			continue
		}
		return verifyMemStoreChain(store, tenantID, fromSeq, toSeq)
	}
	return false, fromSeq, errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"audit ledger: no protocol registered for chain namespace",
		errcode.WithInternal(errcode.InternalAttr("namespace", namespace)))
}

// verifyMemStoreChain snapshots a single (namespace, tenant) chain's entries under
// the store lock, then runs the shared verifyMemChain loop. Extracted to keep the
// lock scope tight and mirror findByIDInMemStore.
func verifyMemStoreChain(store *MemStore, tenantID string, fromSeq, toSeq int64) (bool, int64, error) {
	store.mu.Lock()
	var entries []*Entry
	if chain := store.chains[tenantID]; chain != nil {
		entries = chain.entries
	}
	store.mu.Unlock()
	return verifyMemChain(entries, store.protocol, fromSeq, toSeq)
}
